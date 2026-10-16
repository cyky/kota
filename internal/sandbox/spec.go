// Package sandbox turns session inputs into a docker compose spec.
//
// In a run session the agent container is attached only to an internal
// network with no route out. A forwarder container joins both that network
// and a normal one and forwards a single port to the model gateway, which
// the agent reaches as http://gateway:8080. Prep and fetch sessions have a
// normal network and no forwarder.
package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	// GatewayHost is the forwarder's alias on the internal network.
	GatewayHost = "gateway"
	// GatewayPort is the port the forwarder listens on.
	GatewayPort = 8080
	// Workspace is where the project is mounted inside the agent container.
	Workspace = "/workspace"
	// ReadOnlyRoot holds the read-only reference mounts, one per --ro path.
	ReadOnlyRoot = "/ro"
	// Home is the agent user's HOME, backed by a tmpfs.
	Home = "/home/agent"
	// StateDir holds the agent's state volume. The base image creates it
	// world-writable, and Docker copies that mode into a new volume.
	StateDir = "/var/lib/kota/state"

	// AgentService is the compose service name of the agent container.
	AgentService = "agent"
	// ForwarderService is the compose service name of the forwarder.
	ForwarderService = "forwarder"
	// VolumesService gives new dependency volumes to the session user and
	// lists the empty ones, one target per line.
	VolumesService = "volumes"

	// GoModCache, CorepackHome and PnpmStore hold the caches shared by all
	// projects. The base image creates them world-writable, like StateDir.
	GoModCache   = "/var/lib/kota/gomod"
	CorepackHome = "/var/lib/kota/corepack"
	PnpmStore    = "/var/lib/kota/pnpm-store"

	// GatewayKeyEnv carries the gateway API key into the agent container.
	GatewayKeyEnv = "KOTA_GATEWAY_KEY"

	// EngineHost is the engine forwarder's alias on the internal network:
	// the Docker API and published container ports of the project's Engine
	// VM are reached there.
	EngineHost = "engine"
	// EngineService is the compose service name of the engine forwarder.
	EngineService = "engine"
	// EngineCertDir holds the session's Docker client certificate, read-only.
	EngineCertDir = "/etc/kota/engine"
	// GitExcludes is git's core.excludesFile inside the container, read-only:
	// the project's placeholders and masks, so git add skips them.
	GitExcludes = "/etc/kota/git-excludes"
	// EngineEnv tells the agent of a project with an Engine VM whether this
	// session has it: EngineAvailable, or EngineUnavailable with --no-engine.
	// It is unset in projects without one.
	EngineEnv         = "KOTA_ENGINE"
	EngineAvailable   = "available"
	EngineUnavailable = "unavailable"

	stateKey = "state"

	internalNetwork = "sandbox"
	egressNetwork   = "egress"
)

// Gateway is the host-side model gateway the forwarder connects to.
type Gateway struct {
	Host string
	Port int
}

// ParseGateway parses a gateway URL such as http://host.docker.internal:8686.
// Only plain http is supported: the forwarder is a TCP relay, and the gateway
// lives on the host's loopback.
func ParseGateway(raw string) (Gateway, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Gateway{}, fmt.Errorf("parse gateway URL %q: %w", raw, err)
	}
	if u.Scheme != "http" {
		return Gateway{}, fmt.Errorf("gateway URL %q: scheme must be http", raw)
	}
	if u.Hostname() == "" {
		return Gateway{}, fmt.Errorf("gateway URL %q: missing host", raw)
	}
	if u.Path != "" && u.Path != "/" {
		return Gateway{}, fmt.Errorf("gateway URL %q: must not have a path", raw)
	}
	port := 80
	if p := u.Port(); p != "" {
		port, err = strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return Gateway{}, fmt.Errorf("gateway URL %q: invalid port", raw)
		}
	}
	return Gateway{Host: u.Hostname(), Port: port}, nil
}

// AgentGatewayURL is the gateway base URL as seen from inside the sandbox.
func AgentGatewayURL() string {
	return fmt.Sprintf("http://%s:%d", GatewayHost, GatewayPort)
}

// Mode is what a session is for.
type Mode int

const (
	// ModeRun is an agent session (run, check): offline, with the gateway.
	ModeRun Mode = iota
	// ModePrep installs dependencies: a normal network, so package
	// registries are reachable, and no forwarder or gateway key, so install
	// scripts don't get the models. The shared caches are read-only: install
	// scripts are project code, and must not change what other projects use.
	ModePrep
	// ModeFetch fills the shared caches before ModePrep. It runs only
	// package manager commands, which check what they download against the
	// project's lockfiles, so it is the one mode with writable caches. The
	// dependency volumes must be new: pnpm fetch fills node_modules/.pnpm,
	// and nothing an agent session left there may run while the caches are
	// writable.
	ModeFetch
)

// sharedCache is a volume every project shares. Only ModeFetch writes it,
// so neither an agent nor an install script can plant code in another
// project's dependencies.
type sharedCache struct {
	key, name, target, env string
	// inRun reports whether a run session mounts it.
	inRun bool
}

var sharedCaches = []sharedCache{
	{key: "gomod", name: "kota-cache-gomod", target: GoModCache, env: "GOMODCACHE", inRun: true},
	// corepack keeps the pnpm and yarn releases it downloads here, and the
	// shims need them offline too.
	{key: "corepack", name: "kota-cache-corepack", target: CorepackHome, env: "COREPACK_HOME", inRun: true},
	// pnpm copies packages from the store into node_modules, which is a
	// different volume, so run doesn't need it.
	{key: "pnpm-store", name: "kota-cache-pnpm-store", target: PnpmStore},
}

// SharedCacheVolumes returns the names of the volumes shared by all
// projects.
func SharedCacheVolumes() []string {
	var names []string
	for _, c := range sharedCaches {
		names = append(names, c.name)
	}
	return names
}

// Mount is a bind mount of a host file or directory.
type Mount struct {
	Source string
	Target string
}

// EngineRoute is the agent's route to its project's Engine VM, whose port
// forwards listen on the host's loopback.
type EngineRoute struct {
	// Host is the host as the forwarder reaches it, e.g.
	// host.docker.internal.
	Host string
	// APIPort is the Docker API's port as the agent sees it, one of the
	// Forwards' Listen ports.
	APIPort int
	// Forwards are the agent-side ports and the host ports they relay to:
	// the API and each published port.
	Forwards []PortForward
	// CertDir is the host directory with the client's ca.pem, cert.pem and
	// key.pem, mounted read-only at EngineCertDir.
	CertDir string
}

// PortForward relays Listen on the engine forwarder to Target on the host.
type PortForward struct {
	Listen, Target int
}

// EngineNote is the agent's instruction about its Engine VM, which matches
// EngineEnv: whether it can run the tests that need containers, and, when it
// can't, that it must report them as not run instead of passed. images are
// the declared Engine images of an available Engine VM. The VM can't
// download any other image, and the pull fails with Docker's own network
// error, so the note says how to read that error and what to tell the user.
func EngineNote(available bool, images []string) string {
	if available {
		return "# Containers\n\n" +
			"This session has a container engine, the project's Engine VM (" + EngineEnv + "=" + EngineAvailable + "). " +
			"Docker and testcontainers use it through DOCKER_HOST, so run the project's tests that need containers as part of your checks.\n\n" +
			"It has no network and never downloads images. It has only the images declared under engine.images in kota.yaml, which you can't change: " +
			strings.Join(images, ", ") + ". " +
			"A test or docker command that needs any other image fails when Docker tries to pull it, with an error such as " +
			`"Unable to find image", "lookup registry-1.docker.io", "Temporary failure in name resolution", "no such host", "i/o timeout" or another failure to reach a registry. ` +
			"That means the image isn't declared. testcontainers' error often doesn't name the image: find it in the failing test's code, or the default image of the testcontainers module it uses. " +
			"Don't retry, switch registries or rewrite the test to avoid the image. " +
			"Say which tests couldn't run because of it, and tell the user to add the image (with its tag) under engine.images in kota.yaml and run 'kota prep' on the host.\n"
	}
	return "# Containers\n\n" +
		"This session has no container engine (" + EngineEnv + "=" + EngineUnavailable + "). " +
		"It was started with --no-engine, so the project's Engine VM is off, and Docker and testcontainers don't work. " +
		"Tests and checks that need containers can't run in this session. " +
		"Never report them as passed: when you report your work, list them as not run, and say they need a session with the Engine VM.\n"
}

// Inputs describes one sandbox session.
type Inputs struct {
	Mode Mode
	// ProjectDir is the resolved absolute host path mounted at Workspace.
	ProjectDir string
	UID, GID   int
	Gateway    Gateway
	// GatewayKey reports that a gateway API key is configured. The agent
	// container then takes GatewayKeyEnv from docker compose's environment,
	// so the key itself is never in the spec. Only ModeRun uses it.
	GatewayKey bool

	// AgentImage is the base image or the project's own image.
	AgentImage     string
	ForwarderImage string
	// HelperImage runs VolumesService, the one container with root. It is
	// the base image, never a project image; empty means AgentImage.
	HelperImage string

	// Pinned are read-write mounts of a path over itself. A mount point can't
	// be renamed or removed, so the path stays where read-only mounts inside
	// it expect it.
	Pinned []Mount
	// ReadOnly are extra read-only mounts, e.g. the rendered agent config.
	ReadOnly []Mount
	// StateVolume is the named volume mounted at StateDir, from
	// StateVolume(). XDG data, state and cache point into it, so the agent's
	// sessions, logs and caches outlive the container. Empty means none.
	StateVolume string
	// Deps are the project's dependency volumes: Source is a volume name
	// from DepVolume(), Target a directory under Workspace. They hide what
	// the host has there and are writable in every mode.
	Deps []Mount
	// Scratch are directories inside Home on their own tmpfs, owned by the
	// agent user. Read-only mounts can go inside one while the agent still
	// writes next to them; the contents are discarded with the container.
	Scratch []string
	// Env is passed to the agent container in addition to HOME.
	Env map[string]string
	// Command overrides the agent image's default command when non-empty.
	Command []string
	// Engine, when set, gives a ModeRun session its Engine VM.
	Engine *EngineRoute
	// EngineOff marks a ModeRun session of a project with an Engine VM that
	// runs without it (--no-engine).
	EngineOff bool
}

// ProjectName returns a stable compose project name for a project directory.
func ProjectName(projectDir string) string {
	sum := sha256.Sum256([]byte(projectDir))
	base := strings.ToLower(filepath.Base(projectDir))
	base = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "project"
	}
	return fmt.Sprintf("kota-%s-%s", base, hex.EncodeToString(sum[:])[:8])
}

// StateVolume returns the name of the state volume for an agent in a
// project directory. It is stable for a path and differs across paths; the
// hash is long enough that two projects never share one.
func StateVolume(projectDir, agent string) string {
	sum := sha256.Sum256([]byte(projectDir))
	return fmt.Sprintf("kota-state-%s-%s", agent, hex.EncodeToString(sum[:])[:32])
}

// DepVolume returns the name of the volume for the dependency directory rel
// (relative to the project) of a project directory. All of a project's
// dependency volumes share the prefix DepVolumePrefix(projectDir).
func DepVolume(projectDir, rel string) string {
	sum := sha256.Sum256([]byte(rel))
	return DepVolumePrefix(projectDir) + hex.EncodeToString(sum[:])[:12]
}

// DepVolumePrefix is the name prefix of a project's dependency volumes.
func DepVolumePrefix(projectDir string) string {
	sum := sha256.Sum256([]byte(projectDir))
	return fmt.Sprintf("kota-deps-%s-", hex.EncodeToString(sum[:])[:32])
}

// ProjectVolumes selects existing state and dependency volumes belonging to
// this exact resolved path, including volumes for retired agents/dependencies.
func ProjectVolumes(projectDir string, available []string) []string {
	sum := sha256.Sum256([]byte(projectDir))
	hash := hex.EncodeToString(sum[:])[:32]
	state := regexp.MustCompile(`^kota-state-[a-z0-9][a-z0-9-]*-` + hash + `$`)
	deps := regexp.MustCompile(`^` + regexp.QuoteMeta(DepVolumePrefix(projectDir)) + `[a-f0-9]{12}$`)
	var volumes []string
	for _, name := range available {
		if state.MatchString(name) || deps.MatchString(name) {
			volumes = append(volumes, name)
		}
	}
	slices.Sort(volumes)
	return slices.Compact(volumes)
}

// Build produces the compose spec for a session.
func Build(in Inputs) Compose {
	hardening := Service{
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges:true"},
	}
	// prep is any mode with a normal network and no gateway.
	prep := in.Mode != ModeRun
	fetch := in.Mode == ModeFetch

	// corepack would otherwise add a packageManager field to package.json.
	env := map[string]string{"HOME": Home, "COREPACK_ENABLE_AUTO_PIN": "0"}
	if in.StateVolume != "" {
		env["XDG_DATA_HOME"] = StateDir + "/data"
		env["XDG_STATE_HOME"] = StateDir + "/state"
		env["XDG_CACHE_HOME"] = StateDir + "/cache"
	}
	if prep {
		env["COREPACK_ENABLE_DOWNLOAD_PROMPT"] = "0"
	} else {
		// Fail fast instead of timing out on the missing network.
		env["GOPROXY"] = "off"
		env["COREPACK_ENABLE_NETWORK"] = "0"
	}

	volumes := []Volume{{Type: "bind", Source: in.ProjectDir, Target: Workspace}}
	for _, m := range in.Pinned {
		volumes = append(volumes, Volume{Type: "bind", Source: m.Source, Target: m.Target})
	}
	for _, m := range in.ReadOnly {
		volumes = append(volumes, Volume{Type: "bind", Source: m.Source, Target: m.Target, ReadOnly: true})
	}
	named := map[string]NamedVolume{}
	if in.StateVolume != "" {
		volumes = append(volumes, Volume{Type: "volume", Source: stateKey, Target: StateDir})
		named[stateKey] = NamedVolume{Name: in.StateVolume}
	}
	for _, c := range sharedCaches {
		if !prep && !c.inRun {
			continue
		}
		volumes = append(volumes, Volume{Type: "volume", Source: c.key, Target: c.target, ReadOnly: !fetch})
		named[c.key] = NamedVolume{Name: c.name, External: true}
		if c.env != "" {
			env[c.env] = c.target
		}
	}
	for k, v := range in.Env {
		env[k] = v
	}
	engine := in.Engine
	if prep {
		engine = nil
	}
	if !prep && in.EngineOff && engine == nil {
		env[EngineEnv] = EngineUnavailable
	}
	if engine != nil {
		env[EngineEnv] = EngineAvailable
		// Docker and testcontainers use the Engine VM, with the session's
		// client certificate. Ryuk would need the Docker socket mounted
		// into a container; the Engine VM is reset every session instead.
		env["DOCKER_HOST"] = fmt.Sprintf("tcp://%s:%d", EngineHost, engine.APIPort)
		env["DOCKER_TLS_VERIFY"] = "1"
		env["DOCKER_CERT_PATH"] = EngineCertDir
		env["TESTCONTAINERS_HOST_OVERRIDE"] = EngineHost
		env["TESTCONTAINERS_RYUK_DISABLED"] = "true"
		volumes = append(volumes, Volume{Type: "bind", Source: engine.CertDir, Target: EngineCertDir, ReadOnly: true})
	}

	services := map[string]Service{}
	var depKeys []string
	for i, d := range in.Deps {
		key := fmt.Sprintf("deps-%d", i)
		depKeys = append(depKeys, key)
		volumes = append(volumes, Volume{Type: "volume", Source: key, Target: d.Target})
		named[key] = NamedVolume{Name: d.Source}
	}
	if len(in.Deps) > 0 {
		services[VolumesService] = volumesService(hardening, in, depKeys)
	}
	if len(named) == 0 {
		named = nil
	}

	agent := hardening
	agent.Image = in.AgentImage
	agent.User = fmt.Sprintf("%d:%d", in.UID, in.GID)
	agent.WorkingDir = Workspace
	agent.Environment = env
	if in.GatewayKey && !prep {
		agent.PassEnv = []string{GatewayKeyEnv}
	}
	agent.Volumes = volumes
	for _, dir := range append([]string{Home}, in.Scratch...) {
		agent.Tmpfs = append(agent.Tmpfs, fmt.Sprintf("%s:uid=%d,gid=%d,mode=0700", dir, in.UID, in.GID))
	}
	agent.StdinOpen = true
	agent.Tty = true
	if len(in.Command) > 0 {
		agent.Command = in.Command
	}

	spec := Compose{Name: ProjectName(in.ProjectDir), Volumes: named, Services: services}
	if prep {
		agent.Networks = map[string]*NetworkAttach{egressNetwork: {}}
		spec.Networks = map[string]Network{egressNetwork: {}}
		services[AgentService] = agent
		return spec
	}

	forwarder := hardening
	forwarder.Image = in.ForwarderImage
	forwarder.ReadOnly = true
	forwarder.Command = []string{
		fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr", GatewayPort),
		"TCP:" + net.JoinHostPort(in.Gateway.Host, strconv.Itoa(in.Gateway.Port)),
	}
	forwarder.Networks = map[string]*NetworkAttach{
		internalNetwork: {Aliases: []string{GatewayHost}},
		egressNetwork:   {},
	}
	agent.Networks = map[string]*NetworkAttach{internalNetwork: {}}
	agent.DependsOn = []string{ForwarderService}
	if engine != nil {
		services[EngineService] = engineForwarder(hardening, in.ForwarderImage, engine)
		agent.DependsOn = append(agent.DependsOn, EngineService)
	}
	services[AgentService] = agent
	services[ForwarderService] = forwarder
	spec.Networks = map[string]Network{
		// Isolated gateway mode leaves the bridge without an address, so
		// the Docker VM's own services (e.g. its sshd) can't be reached
		// from the internal network. "internal" alone only blocks routing.
		internalNetwork: {Internal: true, DriverOpts: map[string]string{
			"com.docker.network.bridge.gateway_mode_ipv4": "isolated",
		}},
		egressNetwork: {},
	}
	return spec
}

// engineForwarder relays each of the route's ports from the internal
// network, under the alias EngineHost, to the Engine VM's forwards on the
// host: one socat per port. Like the gateway forwarder, it is the agent's
// only way to those ports.
func engineForwarder(hardening Service, image string, r *EngineRoute) Service {
	const script = `host=$1; shift
while [ $# -gt 0 ]; do
  socat "TCP-LISTEN:$1,fork,reuseaddr" "TCP:$host:$2" &
  shift 2
done
wait`
	s := hardening
	s.Image = image
	s.ReadOnly = true
	s.Entrypoint = []string{"sh", "-c", script, "sh", r.Host}
	for _, f := range r.Forwards {
		s.Entrypoint = append(s.Entrypoint, strconv.Itoa(f.Listen), strconv.Itoa(f.Target))
	}
	s.Networks = map[string]*NetworkAttach{
		internalNetwork: {Aliases: []string{EngineHost}},
		egressNetwork:   {},
	}
	return s
}

// volumesService chowns each dependency volume's root to the session user:
// a new volume mounted under the project starts out owned by root. It then
// prints the agent-side target of each volume that is empty. It runs as
// root with only CAP_CHOWN, no network and without the project.
func volumesService(hardening Service, in Inputs, keys []string) Service {
	const script = `owner=$1; shift
while [ $# -gt 0 ]; do
  chown "$owner" "$1"
  [ -n "$(ls -A "$1")" ] || printf '%s\n' "$2"
  shift 2
done`
	s := hardening
	s.Image = in.HelperImage
	if s.Image == "" {
		s.Image = in.AgentImage
	}
	s.User = "0:0"
	s.CapAdd = []string{"CHOWN"}
	s.ReadOnly = true
	s.NetworkMode = "none"
	s.Command = []string{"sh", "-c", script, "sh", fmt.Sprintf("%d:%d", in.UID, in.GID)}
	for i, d := range in.Deps {
		dir := fmt.Sprintf("/deps/%d", i)
		s.Volumes = append(s.Volumes, Volume{Type: "volume", Source: keys[i], Target: dir})
		s.Command = append(s.Command, dir, d.Target)
	}
	return s
}

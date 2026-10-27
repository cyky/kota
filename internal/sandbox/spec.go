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
	"errors"
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
	// WitnessService runs for the whole session with the agent's project
	// mounts, and nothing else, so its mount table shows at the end which
	// of them still hold. The agent's own containers are gone by then.
	WitnessService = "witness"

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
	// gatewayNetwork is the compose key of the gateway's own Docker
	// network, when it has one.
	gatewayNetwork = "gateway"
)

// Gateway is the model gateway the forwarder connects to.
type Gateway struct {
	Host string
	Port int
	// Network is an existing Docker network the gateway runs on, one
	// CheckGatewayNetwork accepts. The forwarder then joins it instead of
	// egress, and Host is resolved there. Empty means the gateway is
	// reached over egress.
	Network string
}

// Addr is the gateway as host:port, prefixed with its Docker network if it
// has one, which tells gateways apart.
func (g Gateway) Addr() string {
	addr := net.JoinHostPort(g.Host, strconv.Itoa(g.Port))
	if g.Network != "" {
		return g.Network + "/" + addr
	}
	return addr
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

// dockerName is a name Docker accepts for a network.
var dockerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ErrNetworkName is CheckGatewayNetwork's error for a name Docker wouldn't
// accept, as opposed to a network the forwarder must not join.
var ErrNetworkName = errors.New("not a Docker network name")

// CheckGatewayNetwork refuses a Gateway.Network the forwarder must not
// join: a name Docker wouldn't accept (ErrNetworkName), Docker's built-in
// host, none and bridge, and the networks of kota sessions, which would
// connect the forwarder to another session's sandbox.
func CheckGatewayNetwork(name string) error {
	switch {
	case !dockerName.MatchString(name):
		return ErrNetworkName
	case name == "host":
		return errors.New("the forwarder would share the host's network stack")
	case name == "none":
		return errors.New("it has no route to the gateway")
	case name == "bridge":
		return errors.New("the default bridge has no DNS, and holds every container started without a network")
	case strings.HasPrefix(name, projectPrefix):
		return errors.New("a kota session's network")
	}
	return nil
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
	// Mask hides a secret behind an empty file or directory, which the
	// probes check reads as empty.
	Mask bool
}

// Expect is what the containment probes verify about the agent container,
// derived from its volumes.
type Expect struct {
	// ReadOnly must reject writes: every read-only volume.
	ReadOnly []string
	// Pinned must be mount points: every writable bind besides Workspace.
	Pinned []string
	// Masked must read as empty: every Mount with Mask.
	Masked []string
	// State is the agent's state directory, which must be writable. Empty
	// skips the probe.
	State string
	// Mounts are every mount point kota gives the agent container: its
	// volumes and tmpfs directories. Any other mount, besides the
	// container runtime's own, fails the probes.
	Mounts []string
}

// expect derives the probe expectations from the agent's volumes and the
// targets of its masks.
func expect(volumes []Volume, masks []string) Expect {
	var e Expect
	for _, v := range volumes {
		e.Mounts = append(e.Mounts, v.Target)
		switch {
		case v.ReadOnly:
			e.ReadOnly = append(e.ReadOnly, v.Target)
		case v.Type == "volume" && v.Target == StateDir:
			e.State = v.Target
		case v.Type == "bind" && v.Target != Workspace:
			e.Pinned = append(e.Pinned, v.Target)
		}
		if slices.Contains(masks, v.Target) {
			e.Masked = append(e.Masked, v.Target)
		}
	}
	return e
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

// Inputs describes one sandbox session.
type Inputs struct {
	Mode Mode
	// ProjectDir is the resolved absolute host path mounted at Workspace.
	ProjectDir string
	// Key is the project the session belongs to, which names the compose
	// project: the original project of a No-history copy. Empty means
	// ProjectDir.
	Key      string
	UID, GID int
	Gateway  Gateway
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

// projectPrefix starts every ProjectName, and so the name of every kota
// session's networks.
const projectPrefix = "kota-"

// ProjectName returns a stable compose project name for a project directory.
func ProjectName(projectDir string) string {
	sum := sha256.Sum256([]byte(projectDir))
	base := strings.ToLower(filepath.Base(projectDir))
	base = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "project"
	}
	return fmt.Sprintf("%s%s-%s", projectPrefix, base, hex.EncodeToString(sum[:])[:8])
}

// NoHistoryState is the suffix of an agent's state volume name in
// --no-history Sessions, which keeps them apart from the agent's state in
// normal Sessions: transcripts there may hold the history.
const NoHistoryState = "-nohistory"

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

// Build produces the compose spec for a session, and what the probes
// verify about its agent container.
func Build(in Inputs) (Compose, Expect) {
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
	var masks []string
	for _, m := range in.Pinned {
		volumes = append(volumes, Volume{Type: "bind", Source: m.Source, Target: m.Target})
		if m.Mask {
			masks = append(masks, m.Target)
		}
	}
	for _, m := range in.ReadOnly {
		volumes = append(volumes, Volume{Type: "bind", Source: m.Source, Target: m.Target, ReadOnly: true})
		if m.Mask {
			masks = append(masks, m.Target)
		}
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
	tmpfs := append([]string{Home}, in.Scratch...)
	for _, dir := range tmpfs {
		agent.Tmpfs = append(agent.Tmpfs, fmt.Sprintf("%s:uid=%d,gid=%d,mode=0700", dir, in.UID, in.GID))
	}
	agent.StdinOpen = true
	agent.Tty = true
	if len(in.Command) > 0 {
		agent.Command = in.Command
	}

	services[WitnessService] = witnessService(hardening, in, volumes)

	key := in.Key
	if key == "" {
		key = in.ProjectDir
	}
	spec := Compose{Name: ProjectName(key), Volumes: named, Services: services}
	expected := expect(agent.Volumes, masks)
	expected.Mounts = append(expected.Mounts, tmpfs...)
	if prep {
		agent.Networks = map[string]*NetworkAttach{egressNetwork: {}}
		spec.Networks = map[string]Network{egressNetwork: {}}
		services[AgentService] = agent
		return spec, expected
	}

	forwarder := hardening
	forwarder.Image = in.ForwarderImage
	forwarder.ReadOnly = true
	forwarder.Command = []string{
		fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr,backlog=%d", GatewayPort, listenBacklog),
		"TCP:" + net.JoinHostPort(in.Gateway.Host, strconv.Itoa(in.Gateway.Port)),
	}
	spec.Networks = map[string]Network{
		// Isolated gateway mode leaves the bridge without an address, so
		// the Docker VM's own services (e.g. its sshd) can't be reached
		// from the internal network. "internal" alone only blocks routing.
		internalNetwork: {Internal: true, DriverOpts: map[string]string{
			"com.docker.network.bridge.gateway_mode_ipv4": "isolated",
		}},
	}
	// upstream is the network the forwarder reaches the gateway on. A
	// gateway on its own Docker network is reached there, and the
	// forwarder needs no other route.
	upstream := egressNetwork
	if in.Gateway.Network != "" {
		upstream = gatewayNetwork
		spec.Networks[gatewayNetwork] = Network{Name: in.Gateway.Network, External: true}
	}
	forwarder.Networks = map[string]*NetworkAttach{
		internalNetwork: {Aliases: []string{GatewayHost}},
		upstream:        {},
	}
	agent.Networks = map[string]*NetworkAttach{internalNetwork: {}}
	agent.DependsOn = []string{ForwarderService}
	if engine != nil {
		services[EngineService] = engineForwarder(hardening, in.ForwarderImage, engine)
		agent.DependsOn = append(agent.DependsOn, EngineService)
	}
	services[AgentService] = agent
	services[ForwarderService] = forwarder
	for _, s := range services {
		if _, ok := s.Networks[egressNetwork]; ok {
			spec.Networks[egressNetwork] = Network{}
		}
	}
	return spec, expected
}

// listenBacklog is the forwarders' listen backlog. socat's default of 5
// resets connections that arrive together, as Node's testcontainers opens
// them.
const listenBacklog = 1024

// engineForwarder relays each of the route's ports from the internal
// network, under the alias EngineHost, to the Engine VM's forwards on the
// host: one socat per port. Like the gateway forwarder, it is the agent's
// only way to those ports.
func engineForwarder(hardening Service, image string, r *EngineRoute) Service {
	const script = `host=$1; backlog=$2; shift 2
while [ $# -gt 0 ]; do
  socat "TCP-LISTEN:$1,fork,reuseaddr,backlog=$backlog" "TCP:$host:$2" &
  shift 2
done
wait`
	s := hardening
	s.Image = image
	s.ReadOnly = true
	s.Entrypoint = []string{"sh", "-c", script, "sh", r.Host, strconv.Itoa(listenBacklog)}
	for _, f := range r.Forwards {
		s.Entrypoint = append(s.Entrypoint, strconv.Itoa(f.Listen), strconv.Itoa(f.Target))
	}
	s.Networks = map[string]*NetworkAttach{
		internalNetwork: {Aliases: []string{EngineHost}},
		egressNetwork:   {},
	}
	return s
}

// witnessService holds the agent's binds at Workspace and under it, and
// only waits. A protection mount is dropped in every container at once
// when the path under it is replaced on the host, so its mount table
// stands for the agent's. It has no network and runs no project code.
func witnessService(hardening Service, in Inputs, volumes []Volume) Service {
	s := hardening
	s.Image = in.HelperImage
	if s.Image == "" {
		s.Image = in.AgentImage
	}
	s.User = fmt.Sprintf("%d:%d", in.UID, in.GID)
	s.ReadOnly = true
	s.NetworkMode = "none"
	s.Entrypoint = []string{"sh", "-c", `trap 'exit 0' TERM; sleep infinity & wait`}
	for _, v := range volumes {
		if v.Type == "bind" && (v.Target == Workspace || strings.HasPrefix(v.Target, Workspace+"/")) {
			s.Volumes = append(s.Volumes, v)
		}
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

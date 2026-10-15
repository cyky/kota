// Command localllm runs coding agents in an offline Docker sandbox that can
// reach only the local model gateway.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"localllm/assets"
	"localllm/internal/approval"
	"localllm/internal/changereport"
	"localllm/internal/check"
	"localllm/internal/deps"
	"localllm/internal/docker"
	"localllm/internal/engine"
	"localllm/internal/enginevm"
	"localllm/internal/gatewaykey"
	"localllm/internal/interrupt"
	"localllm/internal/mask"
	"localllm/internal/models"
	"localllm/internal/opencode"
	"localllm/internal/pathpolicy"
	"localllm/internal/pi"
	"localllm/internal/prepgate"
	"localllm/internal/probe"
	"localllm/internal/projectimage"
	"localllm/internal/sandbox"
	"localllm/internal/sessionregistry"
)

const (
	baseImage      = "localllm-base:latest"
	forwarderImage = "localllm-forwarder:latest"
	defaultGateway = "http://host.docker.internal:8080"
	defaultAgent   = opencode.Agent
	// engineHostAlias is the host as the engine forwarder reaches it, where
	// QEMU's port forwards listen on 127.0.0.1.
	engineHostAlias = "host.docker.internal"
	// engineBootTimeout bounds the Engine VM's boot to a ready Docker API.
	engineBootTimeout = 60 * time.Second

	opencodeConfigPath = "/etc/localllm/opencode.json"
	// piAgentDir is pi's config directory. It is writable, because pi
	// creates lock files next to its settings, and discarded after the
	// session; the rendered settings.json and models.json in it are
	// read-only.
	piAgentDir   = sandbox.Home + "/.pi/agent"
	piSessionDir = sandbox.StateDir + "/pi/sessions"
)

const usage = `usage: localllm <command> [flags]

commands:
  build                 build the sandbox images and, with QEMU installed,
                        the Engine base disk
  build dir             build dir's image from its ` + projectimage.File + `, on top
                        of the base image; prep, run, shell and check use it
  prep [dir]            install dir's dependencies with network access and
                        no agent, into volumes that run mounts offline
  run [flags] [dir] [-- cmd...]
                        verify containment, then start the agent offline in
                        dir (default: current dir)
  shell [flags] [dir]   verify containment, then open an interactive Bash
                        shell in the same sandbox as run
  check [flags] [dir]   show mounts and run the containment probes
  clean [flags] [dir]   remove project state and dependency volumes after
                        confirmation

flags:
  --agent NAME          opencode (default) or pi; pi has no permission
                        system and always runs in full auto
  --gateway URL         model gateway (default $LOCALLLM_GATEWAY or ` + defaultGateway + `)
  --model ID            model to start with (default: models.yaml's default);
                        the agent is offered every model the gateway serves
  --ro PATH             mount PATH read-only at ` + sandbox.ReadOnlyRoot + `/<name> (repeatable)
  --auto                run only: opencode approves edits and commands without
                        asking; on uncommitted changes, asks you to type y
  --unsafe-skip-check   run only: start without the containment probes
  --shared-caches       clean only: also reset shared caches; all sessions
                        must be idle

dir must be inside the allowed root ($LOCALLLM_ALLOWED_ROOT, default ~/Projects).
A gateway API key (llama-swap apiKeys) is read from $LOCALLLM_GATEWAY_KEY or
the file named by $LOCALLLM_GATEWAY_KEY_FILE.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	// A signal stops localllm at the next step, after which it tears down as
	// on an error.
	interrupt.Catch()
	var err error
	switch os.Args[1] {
	case "build":
		err = cmdBuild(os.Args[2:])
	case "prep":
		err = cmdPrep(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "shell":
		err = cmdShell(os.Args[2:])
	case "check":
		err = cmdCheck(os.Args[2:])
	case "clean":
		err = cmdClean(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if interrupt.Err() != nil {
		// Whatever failed, it failed because of the signal.
		fmt.Fprintln(os.Stderr, "localllm: interrupted")
		os.Exit(130)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "localllm:", err)
		os.Exit(1)
	}
}

func cmdBuild(args []string) error {
	fset := flag.NewFlagSet("build", flag.ContinueOnError)
	if err := fset.Parse(args); err != nil {
		return err
	}
	switch fset.NArg() {
	case 0:
		return buildBase()
	case 1:
		return buildProject(fset.Arg(0))
	}
	return errors.New("build takes at most one directory")
}

// buildBase builds the images every project shares.
func buildBase() error {
	images := map[string]string{"base": baseImage, "forwarder": forwarderImage}
	for _, name := range []string{"forwarder", "base"} {
		ctx, err := fs.Sub(assets.Images, "images/"+name)
		if err != nil {
			return err
		}
		fmt.Printf("building %s\n", images[name])
		if err := docker.BuildImage(images[name], ctx); err != nil {
			return fmt.Errorf("build %s: %w", images[name], err)
		}
	}
	layout, err := engineLayout()
	if err != nil {
		return err
	}
	err = enginevm.BuildBase(layout, engineHost(), os.Stdout)
	if errors.Is(err, enginevm.ErrNoQEMU) {
		fmt.Printf("skipping the Engine base disk: %v. Projects with an engine section in %s need it.\n", err, engine.ConfigFile)
		return nil
	}
	return err
}

// engineLayout is where engine data lives: the Engine base disk and each
// project's image archives, in the user cache dir with localllm's other
// host state, outside every project and sandbox.
func engineLayout() (engine.Layout, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return engine.Layout{}, err
	}
	return engine.Layout{Root: filepath.Join(cache, "localllm", "engine")}, nil
}

func engineHost() engine.Host { return engine.Host{OS: runtime.GOOS, Arch: runtime.GOARCH} }

// buildProject builds a project's image from its Dockerfile on top of the
// base image.
func buildProject(dir string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	paths, err := pathpolicy.Validate(dir, nil, pathpolicy.Default(home, os.Getenv("LOCALLLM_ALLOWED_ROOT")))
	if err != nil {
		return err
	}
	project := paths.Project
	dockerfile, err := projectimage.Read(project)
	if err != nil {
		return err
	}
	if dockerfile == nil {
		return fmt.Errorf("no %s in %s; without one, sessions use the base image", projectimage.File, project)
	}
	baseID, _, ok, err := inspectImage(baseImage)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("the base image %s is missing; run 'localllm build' first", baseImage)
	}
	tag := projectimage.Tag(project)
	fmt.Printf("building %s from %s (no build context: COPY and ADD can't read the project)\n", tag, filepath.Join(project, projectimage.File))
	if err := docker.BuildDockerfile(tag, dockerfile, projectimage.Labels(dockerfile, baseID)); err != nil {
		return fmt.Errorf("build %s: %w", tag, err)
	}
	return nil
}

// inspectImage is docker.InspectImage, replaced in tests.
var inspectImage = docker.InspectImage

// chooseImage returns the image for a session of project and a warning when
// the project's own image is missing or stale.
func chooseImage(project string) (projectimage.Choice, error) {
	dockerfile, err := projectimage.Read(project)
	if err != nil || dockerfile == nil {
		return projectimage.Choice{Image: baseImage}, err
	}
	baseID, _, _, err := inspectImage(baseImage)
	if err != nil {
		return projectimage.Choice{}, err
	}
	_, labels, exists, err := inspectImage(projectimage.Tag(project))
	if err != nil {
		return projectimage.Choice{}, err
	}
	return projectimage.Choose(project, baseImage, dockerfile, projectimage.Image{Exists: exists, Labels: labels}, baseID), nil
}

// agentSetup is how a session runs one agent.
type agentSetup struct {
	name string
	// configs are the in-sandbox paths of the config files render returns,
	// each mounted read-only.
	configs []string
	// scratch are writable per-session directories (sandbox.Inputs.Scratch).
	scratch []string
	env     map[string]string
	command []string
	// render returns the content of each of configs, in order. keyEnv,
	// when set, names the variable holding the gateway key.
	render func(c models.Catalog, auto bool, keyEnv string) ([][]byte, error)
	// approval describes, for the startup message, whether the agent asks
	// before it acts.
	approval func(auto bool) string
}

func agentFor(name string) (agentSetup, error) {
	switch name {
	case opencode.Agent:
		return agentSetup{
			name:    opencode.Agent,
			configs: []string{opencodeConfigPath},
			env:     opencode.Env(opencodeConfigPath),
			command: []string{"opencode"},
			render: func(c models.Catalog, auto bool, keyEnv string) ([][]byte, error) {
				base, err := assets.Agents.ReadFile(opencode.BaseConfig)
				if err != nil {
					return nil, err
				}
				cfg, err := opencode.Render(base, opencode.Settings{GatewayURL: sandbox.AgentGatewayURL(), Models: c, Auto: auto, APIKeyEnv: keyEnv})
				return [][]byte{cfg}, err
			},
			approval: func(auto bool) string {
				if auto {
					return "auto (opencode edits files and runs commands without asking)"
				}
				return "ask (opencode asks before edits and commands; --auto to skip)"
			},
		}, nil
	case pi.Agent:
		return agentSetup{
			name:    pi.Agent,
			configs: []string{piAgentDir + "/settings.json", piAgentDir + "/models.json"},
			scratch: []string{piAgentDir},
			env:     pi.Env(piAgentDir, piSessionDir),
			command: []string{"pi"},
			render: func(c models.Catalog, _ bool, keyEnv string) ([][]byte, error) {
				base, err := assets.Agents.ReadFile(pi.BaseSettings)
				if err != nil {
					return nil, err
				}
				s := pi.Settings{GatewayURL: sandbox.AgentGatewayURL(), Models: c, APIKeyEnv: keyEnv}
				settings, err := pi.RenderSettings(base, s)
				if err != nil {
					return nil, err
				}
				modelsCfg, err := pi.RenderModels(s)
				return [][]byte{settings, modelsCfg}, err
			},
			approval: func(bool) string {
				return "auto (pi has no permission system: it edits files and runs commands without asking, and the sandbox is its only boundary)"
			},
		}, nil
	}
	return agentSetup{}, fmt.Errorf("unknown agent %q; available: %s, %s", name, opencode.Agent, pi.Agent)
}

// session holds everything needed to start one sandbox.
type session struct {
	shell bool
	spec  sandbox.Compose
	// fetchSpec is prep's fetch phase, which fills the shared caches before
	// spec installs.
	fetchSpec  sandbox.Compose
	gateway    sandbox.Gateway
	sessionDir string
	project    string
	layout     mask.Layout
	agent      agentSetup
	// gatewayKey is the gateway API key, empty when none is configured. It
	// only reaches the sandbox through docker compose's environment.
	gatewayKey string
	// cfgFiles are the host files behind agent.configs, written by
	// configure once the gateway's models are known.
	cfgFiles []string
	// mounts are what the probes verify.
	mounts check.Mounts
	// deps is how prep installs the project's dependencies.
	deps deps.Plan
	// mountPoints are directories Docker creates on the host for the
	// dependency volumes, outermost first.
	mountPoints []string
	// image is the agent container's image: the base image or the
	// project's own.
	image projectimage.Choice
	// engine is the session's Engine VM, nil when it has none.
	engine *engineSession
}

// engineSession is a session's Engine VM: worked out by prepareEngine,
// started by start and stopped by stop.
type engineSession struct {
	vm         engine.VM
	cmd        engine.Command
	certs      engine.Certs
	archiveDir string
	archives   []engine.Archive
	// warning says the Engine base disk should be rebuilt.
	warning string
	running *enginevm.VM
	started time.Time
}

// prepareEngine works out project's Engine VM for a session: the Engine
// base disk, the prepped archives, host ports, and TLS certificates made
// for this session in sessionDir. It returns nil when the project has no
// engine section, and an error with the fix when the VM can't start.
func prepareEngine(project, sessionDir string) (*engineSession, error) {
	layout, err := engineLayout()
	if err != nil {
		return nil, err
	}
	cfg, err := engine.Read(project)
	if err != nil {
		// A broken localllm.yaml shouldn't block a project that never used
		// an engine, but an engine project can't tell which images to load.
		if rec, rerr := engine.LoadRecord(layout.ArchiveDir(project)); rerr == nil && rec == nil {
			return nil, nil
		}
		return nil, fmt.Errorf("%w; the Engine VM can't start without knowing its images", err)
	}
	if cfg == nil {
		return nil, nil
	}
	host := engineHost()
	m, err := engine.MachineFor(host)
	if err != nil {
		return nil, err
	}
	if _, err := enginevm.FindQEMU(m); err != nil {
		return nil, fmt.Errorf("%s declares engine images, but %w", engine.ConfigFile, err)
	}
	base, warning, err := layout.LoadBase(host.Arch)
	if err != nil {
		return nil, err
	}
	rec, err := engine.LoadRecord(layout.ArchiveDir(project))
	if err != nil {
		return nil, err
	}
	archives, err := rec.Select(cfg.Images)
	if err != nil {
		return nil, fmt.Errorf("%w; run 'localllm prep %s'", err, project)
	}
	ports, err := enginevm.FreePorts(1 + len(engine.PublishedPorts()))
	if err != nil {
		return nil, err
	}
	certs, err := engine.NewCerts(time.Now(), []string{sandbox.EngineHost, "127.0.0.1"})
	if err != nil {
		return nil, err
	}
	server, err := certs.WriteServer(filepath.Join(sessionDir, "engine-server"))
	if err != nil {
		return nil, err
	}
	vm := engine.VM{
		Project:   project,
		Host:      host,
		CPUs:      cfg.CPUs,
		MemoryMiB: cfg.MemoryMiB,
		Base:      base,
		Certs:     server,
		Ports:     engine.Ports{API: ports[0], Published: ports[1:]},
		Serial:    filepath.Join(sessionDir, "engine-console.log"),
		Pidfile:   layout.Pidfile(project),
	}
	cmd, err := engine.Build(vm)
	if err != nil {
		return nil, err
	}
	return &engineSession{vm: vm, cmd: cmd, certs: certs, archiveDir: layout.ArchiveDir(project), archives: archives, warning: warning}, nil
}

// route writes the client certificate into dir and returns the sandbox's
// route to the VM. The agent sees the VM's own port numbers.
func (e *engineSession) route(dir string) (*sandbox.EngineRoute, error) {
	if err := e.certs.WriteClient(dir); err != nil {
		return nil, err
	}
	fwds, err := e.vm.Ports.Forwards()
	if err != nil {
		return nil, err
	}
	r := &sandbox.EngineRoute{Host: engineHostAlias, APIPort: engine.GuestAPIPort, CertDir: dir}
	for _, f := range fwds {
		r.Forwards = append(r.Forwards, sandbox.PortForward{Listen: f.Guest, Target: f.Host})
	}
	return r, nil
}

// start boots the VM without waiting for it, so it boots while the
// sandbox starts.
func (e *engineSession) start() error {
	vm, err := enginevm.Start(e.cmd, e.vm, e.certs.CA, e.certs.ClientCert, e.certs.ClientKey)
	if err != nil {
		return err
	}
	e.running, e.started = vm, time.Now()
	return nil
}

// ready waits for the VM's Docker API and loads the archives.
func (e *engineSession) ready(w io.Writer) error {
	if err := e.running.WaitReady(engineBootTimeout); err != nil {
		return err
	}
	booted := time.Since(e.started)
	loadStart := time.Now()
	if err := e.running.Load(e.archiveDir, e.archives); err != nil {
		return err
	}
	fmt.Fprintf(w, "Engine VM ready: booted in %s, loaded %d image(s) in %s\n",
		booted.Round(100*time.Millisecond), len(e.archives), time.Since(loadStart).Round(100*time.Millisecond))
	return nil
}

func (e *engineSession) stop() {
	if e.running != nil {
		e.running.Stop()
	}
}

// printWarnings prints the layout's notes and warnings, and which image
// the session uses when it isn't the base image or should be rebuilt.
func (s *session) printWarnings(w io.Writer) {
	for _, n := range s.layout.Notes {
		fmt.Fprintln(w, "note:", n)
	}
	if s.image.Image != baseImage {
		fmt.Fprintf(w, "note: using the project image %s, built from %s\n", s.image.Image, projectimage.File)
	}
	for _, warning := range s.layout.Warnings {
		fmt.Fprintln(w, "WARNING:", warning)
	}
	if s.image.Warning != "" {
		fmt.Fprintln(w, "WARNING:", s.image.Warning)
	}
	if s.deps.Fallback != "" {
		fmt.Fprintln(w, "WARNING:", s.deps.Fallback)
	}
	if s.engine != nil && s.engine.warning != "" {
		fmt.Fprintln(w, "WARNING:", s.engine.warning)
	}
}

// cleanup removes what the session left on the host. Call it after the
// containers are gone.
func (s *session) cleanup() {
	// Innermost first; a directory with content is kept.
	for _, dir := range slices.Backward(s.mountPoints) {
		os.Remove(dir)
	}
	s.layout.Cleanup(s.project)
	os.RemoveAll(s.sessionDir)
}

// sessionArgs are the [flags] [dir] [-- cmd...] shared by run, shell and check.
type sessionArgs struct {
	exclusiveCaches bool
	shell           bool
	// engine starts the project's Engine VM, if it declares one.
	engine     bool
	agent      string
	gatewayURL string
	model      string
	dir        string
	ro         []string
	command    []string
}

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string     { return fmt.Sprint(*l) }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

func parseSessionArgs(name string, args []string, register func(*flag.FlagSet)) (sessionArgs, error) {
	fset := flag.NewFlagSet(name, flag.ContinueOnError)
	agent := fset.String("agent", defaultAgent, "agent `NAME`: opencode or pi")
	gw := fset.String("gateway", envOr("LOCALLLM_GATEWAY", defaultGateway), "model gateway URL")
	model := fset.String("model", "", "model `ID` to start with")
	var ro stringList
	fset.Var(&ro, "ro", "mount `PATH` read-only (repeatable)")
	if register != nil {
		register(fset)
	}
	// Split off the command first: the flag package would swallow a "--"
	// that comes before any directory.
	var command []string
	if i := slices.Index(args, "--"); i >= 0 {
		args, command = args[:i], args[i+1:]
	}
	// Flags may come before or after the directory.
	var dirs []string
	for {
		if err := fset.Parse(args); err != nil {
			return sessionArgs{}, err
		}
		if fset.NArg() == 0 {
			break
		}
		dirs = append(dirs, fset.Arg(0))
		args = fset.Args()[1:]
	}

	sa := sessionArgs{agent: *agent, gatewayURL: *gw, model: *model, dir: ".", ro: ro, command: command}
	switch len(dirs) {
	case 0:
	case 1:
		sa.dir = dirs[0]
	default:
		return sessionArgs{}, fmt.Errorf("%s takes at most one directory", name)
	}
	return sa, nil
}

// resolveSessionPaths validates the project and read-only mounts once, before
// taking the lease. Preparation must use these exact paths rather than resolve
// the original CLI arguments again after a symlink might have changed.
func resolveSessionPaths(sa sessionArgs) (pathpolicy.Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return pathpolicy.Paths{}, err
	}
	return pathpolicy.Validate(sa.dir, sa.ro, pathpolicy.Default(home, os.Getenv("LOCALLLM_ALLOWED_ROOT")))
}

// detectDeps works out the project's dependency plan. It only reads the
// project, so it can run before the user is asked anything.
func detectDeps(project string) (deps.Plan, error) {
	return deps.Detect(project, deps.Options{PnpmStore: sandbox.PnpmStore})
}

// sessionDeps is detectDeps for run, shell and check. When detection fails,
// e.g. on a manifest an agent left broken, the session still starts so the
// file can be fixed, with the fallback volumes of deps.Fallback. Only prep
// refuses.
func sessionDeps(project string) (deps.Plan, error) {
	plan, err := detectDeps(project)
	if err == nil {
		return plan, nil
	}
	last, lerr := prepgate.Load(prepRecordFile(project))
	if lerr != nil {
		return deps.Plan{}, fmt.Errorf("%w; read prep record: %v", err, lerr)
	}
	var dirs []deps.Dir
	if last != nil {
		dirs = last.Dirs
	}
	return deps.Fallback(project, dirs, err)
}

// prepareSession builds the compose spec from the paths covered by the lease
// and depPlan from detectDeps, which the user may have confirmed. It writes
// to the host (the session dir, seeded protections), so confirmations come
// first. The agent config is written later by configure. The caller removes
// sessionDir when done. Prep has no agent, gateway or key and runs the
// dependency install.
func prepareSession(sa sessionArgs, mode sandbox.Mode, paths pathpolicy.Paths, depPlan deps.Plan) (*session, error) {
	prep := mode == sandbox.ModePrep
	var agent agentSetup
	var err error
	if !prep {
		if agent, err = agentFor(sa.agent); err != nil {
			return nil, err
		}
	}
	project := paths.Project
	var gw sandbox.Gateway
	var key string
	if !prep {
		if gw, err = sandbox.ParseGateway(sa.gatewayURL); err != nil {
			return nil, err
		}
		if key, err = gatewaykey.Load(os.Getenv); err != nil {
			return nil, err
		}
	}
	layout, err := mask.Plan(project, mask.DefaultRules)
	if err != nil {
		return nil, err
	}
	image, err := chooseImage(project)
	if err != nil {
		return nil, err
	}
	var protected, pinnedPaths []string
	for _, m := range layout.Mounts {
		if m.Pin {
			pinnedPaths = append(pinnedPaths, m.Path)
		} else {
			protected = append(protected, m.Path)
		}
	}
	if err := depPlan.Conflict(protected, pinnedPaths); err != nil {
		return nil, err
	}

	sessionDir, err := newSessionDir(project)
	if err != nil {
		return nil, err
	}
	// Created empty so each mount source is a file; configure fills them.
	var cfgMounts []sandbox.Mount
	var cfgFiles []string
	err = os.Mkdir(filepath.Join(sessionDir, "agent"), 0o755)
	for _, target := range agent.configs {
		if err != nil {
			break
		}
		file := filepath.Join(sessionDir, "agent", path.Base(target))
		err = os.WriteFile(file, nil, 0o644)
		cfgFiles = append(cfgFiles, file)
		cfgMounts = append(cfgMounts, sandbox.Mount{Source: file, Target: target})
	}
	if err != nil {
		os.RemoveAll(sessionDir)
		return nil, err
	}

	sess := &session{shell: sa.shell, gateway: gw, sessionDir: sessionDir, project: project, layout: layout, agent: agent, gatewayKey: key, cfgFiles: cfgFiles, deps: depPlan, image: image}
	var engineRoute *sandbox.EngineRoute
	if sa.engine && !prep {
		sess.engine, err = prepareEngine(project, sessionDir)
		if err == nil && sess.engine != nil {
			engineRoute, err = sess.engine.route(filepath.Join(sessionDir, "engine-client"))
		}
		if err != nil {
			os.RemoveAll(sessionDir)
			return nil, err
		}
	}
	var depMounts []sandbox.Mount
	for _, d := range depPlan.Dirs {
		sess.mountPoints = append(sess.mountPoints, missingDirs(project, d.Path)...)
		depMounts = append(depMounts, sandbox.Mount{Source: sandbox.DepVolume(project, d.Path), Target: sandbox.Workspace + "/" + d.Path})
	}
	pinned, protectedMounts, err := layout.Bind(project, sessionDir)
	if err == nil {
		err = layout.Prepare(project)
	}
	if err != nil {
		sess.cleanup()
		return nil, err
	}
	sess.mounts.Pinned, sess.mounts.ReadOnly, sess.mounts.Masked = layout.Targets()
	sess.mounts.ReadOnly = append(sess.mounts.ReadOnly, agent.configs...)
	if !prep {
		sess.mounts.State = sandbox.StateDir
	}
	for _, m := range paths.ReadOnly {
		sess.mounts.ReadOnly = append(sess.mounts.ReadOnly, m.Target)
	}
	if engineRoute != nil {
		sess.mounts.ReadOnly = append(sess.mounts.ReadOnly, sandbox.EngineCertDir)
	}

	env := map[string]string{}
	maps.Copy(env, agent.env)
	maps.Copy(env, gitEnv(project))
	command := sa.command
	if len(command) == 0 {
		command = agent.command
	}
	if sa.shell {
		command = []string{"bash", "--noprofile", "--norc", "-i"}
	}
	stateVolume := ""
	if prep {
		command = []string{"sh", "-c", depPlan.Script()}
	} else {
		stateVolume = sandbox.StateVolume(project, agent.name)
	}
	in := sandbox.Inputs{
		ProjectDir:     project,
		UID:            os.Getuid(),
		GID:            os.Getgid(),
		Mode:           mode,
		Gateway:        gw,
		GatewayKey:     key != "",
		AgentImage:     image.Image,
		ForwarderImage: forwarderImage,
		HelperImage:    baseImage,
		Pinned:         pinned,
		ReadOnly:       append(append(cfgMounts, protectedMounts...), paths.ReadOnly...),
		Scratch:        agent.scratch,
		StateVolume:    stateVolume,
		Deps:           depMounts,
		Env:            env,
		Command:        command,
		Engine:         engineRoute,
	}
	sess.spec = sandbox.Build(in)
	if prep {
		in.Mode = sandbox.ModeFetch
		in.Command = []string{"sh", "-c", depPlan.FetchScript()}
		sess.fetchSpec = sandbox.Build(in)
	}
	return sess, nil
}

// missingDirs returns the directories on the way to the dependency
// directory rel that don't exist yet, outermost first. Docker creates them
// on the host as mount points.
func missingDirs(project, rel string) []string {
	var missing []string
	p := project
	for _, part := range strings.Split(rel, "/") {
		p = filepath.Join(p, part)
		if len(missing) > 0 {
			missing = append(missing, p)
		} else if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			missing = append(missing, p)
		}
	}
	return missing
}

// gitEnv lets git work inside the sandbox without writing config there:
// files on the shared mount can appear owned by another user, and commits
// need the host identity. GIT_CONFIG_* is command-line scope, which git
// trusts for safe.directory.
func gitEnv(project string) map[string]string {
	settings := [][2]string{{"safe.directory", "*"}}
	for _, key := range []string{"user.name", "user.email"} {
		out, err := exec.Command("git", "-C", project, "config", "--get", key).Output()
		if v := strings.TrimSpace(string(out)); err == nil && v != "" {
			settings = append(settings, [2]string{key, v})
		}
	}
	env := map[string]string{"GIT_CONFIG_COUNT": strconv.Itoa(len(settings))}
	for i, kv := range settings {
		env["GIT_CONFIG_KEY_"+strconv.Itoa(i)] = kv[0]
		env["GIT_CONFIG_VALUE_"+strconv.Itoa(i)] = kv[1]
	}
	return env
}

func cmdRun(args []string) error {
	var skipCheck, auto bool
	sa, err := parseSessionArgs("run", args, func(f *flag.FlagSet) {
		f.BoolVar(&skipCheck, "unsafe-skip-check", false, "start without running the containment probes")
		f.BoolVar(&auto, "auto", false, "approve edits and commands without asking")
	})
	if err != nil {
		return err
	}
	return cmdOffline(sa, auto, skipCheck)
}

func cmdShell(args []string) error {
	sa, err := parseSessionArgs("shell", args, nil)
	if err != nil {
		return err
	}
	if len(sa.command) > 0 {
		return errors.New("shell does not take a command; use 'localllm run [dir] -- cmd...' instead")
	}
	sa.shell = true
	sa.engine = true
	return cmdOffline(sa, false, false)
}

// lockedProject keeps the validated mount paths with the lease that covers them.
type lockedProject struct {
	*sessionregistry.Lease
	paths pathpolicy.Paths
}

// lockProject runs before prepareSession can create protection placeholders or
// touch dependency volumes. check also needs a lease: it uses the same compose
// project and mounts, and would otherwise tear down a running session.
func lockProject(sa sessionArgs, operation string) (*lockedProject, error) {
	paths, err := resolveSessionPaths(sa)
	if err != nil {
		return nil, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	r := &sessionregistry.Registry{
		Dir: filepath.Join(cache, "localllm", "registry"),
		Recover: func(project string) error {
			layout, err := engineLayout()
			if err != nil {
				return err
			}
			if err := enginevm.Recover(layout.Pidfile(project)); err != nil {
				return err
			}
			return docker.RecoverProject(sandbox.ProjectName(project))
		},
	}
	if sa.agent != "" {
		operation += " (" + sa.agent + ")"
	}
	acquire := r.Acquire
	if sa.exclusiveCaches {
		acquire = r.AcquireExclusive
	}
	lease, err := acquire(paths.Project, operation, "")
	if err != nil {
		return nil, err
	}
	return &lockedProject{Lease: lease, paths: paths}, nil
}

func releaseProject(lease *lockedProject) {
	if err := lease.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "WARNING: release project session:", err)
	}
}

// cmdOffline shares startup, cleanup and change reporting for run and shell.
func cmdOffline(sa sessionArgs, auto, skipCheck bool) error {
	operation := "run"
	if sa.shell {
		operation = "shell"
	}
	lease, err := lockProject(sa, operation)
	if err != nil {
		return err
	}
	defer releaseProject(lease)
	// Fail on an unknown agent or a broken models.yaml before asking.
	agent, err := agentFor(sa.agent)
	if err != nil {
		return err
	}
	if _, err := models.ParseFile(assets.Models); err != nil {
		return err
	}
	depPlan, err := sessionDeps(lease.paths.Project)
	if err != nil {
		return err
	}
	if !sa.shell {
		if err := confirmFullAuto(lease.paths.Project, agent.name, auto); err != nil {
			return err
		}
	}
	sess, err := prepareSession(sa, sandbox.ModeRun, lease.paths, depPlan)
	if err != nil {
		return err
	}
	// Snapshot before Docker creates any mount points, and compare after
	// cleanup has removed them, so placeholders aren't reported.
	before, err := changereport.SnapshotWith(sess.project, sess.layout.Watch)
	if err != nil {
		sess.cleanup()
		return fmt.Errorf("snapshot watched files: %w", err)
	}
	err = runSession(sess, lease.Lease, sa.model, auto, skipCheck)
	sess.cleanup()
	reportChanges(os.Stderr, sess.project, sess.layout.Watch, before)
	return err
}

// runSession starts the sandbox, verifies it unless skipCheck, and runs the
// agent or shell. The containers are gone when it returns.
func runSession(sess *session, lease *sessionregistry.Lease, model string, auto, skipCheck bool) error {
	ds, stop, err := startSession(sess, lease, model, auto, skipCheck)
	if err != nil {
		return err
	}
	defer stop()
	return ds.Interactive()
}

// startSession starts and verifies the sandbox and the Engine VM, if the
// session has one, up to where the agent or shell runs. stop tears both
// down; on an error, nothing is left running.
func startSession(sess *session, lease *sessionregistry.Lease, model string, auto, skipCheck bool) (ds *docker.Session, stop func(), err error) {
	var stops []func()
	teardown := func() {
		for _, f := range slices.Backward(stops) {
			f()
		}
	}
	defer func() {
		if err != nil {
			teardown()
		}
	}()
	sess.printWarnings(os.Stderr)
	if n := len(sess.mounts.Masked); n > 0 {
		fmt.Fprintf(os.Stderr, "masked %d secret path(s); run 'localllm check' to list them\n", n)
	}

	// The Engine VM boots while the sandbox starts, and stops after the
	// sandbox's containers are gone.
	if sess.engine != nil {
		if err := sess.engine.start(); err != nil {
			return nil, nil, err
		}
		stops = append(stops, sess.engine.stop)
	}
	ds, err = docker.Start(sess.spec, sess.sessionDir, sess.gatewayKey)
	if err != nil {
		return nil, nil, err
	}
	stops = append(stops, ds.Close)
	if sess.engine != nil {
		// Teardown runs stops last to first, so this disarms the watch
		// before the sandbox and the VM go.
		closing := make(chan struct{})
		stops = append(stops, func() { close(closing) })
		go watchEngine(sess.engine.running.Done(), closing, ds)
	}
	printEmptyDeps(os.Stderr, sess, ds.EmptyDeps())

	catalog, err := sess.configure(ds, model, auto)
	if err != nil {
		return nil, nil, err
	}
	if sess.engine != nil {
		if err := sess.engine.ready(os.Stderr); err != nil {
			return nil, nil, err
		}
	}
	if !sess.shell {
		clashes, err := lease.SetModel(catalog.Default)
		if err != nil {
			fmt.Fprintln(os.Stderr, "WARNING: could not check for model clashes:", err)
		} else {
			printModelClashes(os.Stderr, catalog.Default, clashes)
		}
	}
	for _, n := range catalog.Notes {
		fmt.Fprintln(os.Stderr, "note:", n)
	}
	fmt.Fprintf(os.Stderr, "model: %s (%d offered)\n", catalog.Default, len(catalog.Models))
	if sess.shell {
		fmt.Fprintln(os.Stderr, "starting an interactive Bash shell (no agent)")
	} else {
		fmt.Fprintln(os.Stderr, "approval:", sess.agent.approval(auto))
	}

	if skipCheck {
		fmt.Fprintln(os.Stderr, "WARNING: --unsafe-skip-check: containment probes skipped; the sandbox is NOT verified.")
	} else {
		results, err := check.Run(ds, sess.gateway, sess.mounts)
		if err != nil {
			return nil, nil, fmt.Errorf("containment probes: %w", err)
		}
		if failed := probe.Failed(results); len(failed) > 0 {
			printResults(os.Stderr, failed)
			return nil, nil, fmt.Errorf("refusing to start: %d of %d containment probes failed (run 'localllm check' for details)", len(failed), len(results))
		}
		fmt.Fprintf(os.Stderr, "containment verified: %d probes passed\n", len(results))
	}
	// A probe cut short by a signal may still have produced output.
	if err := interrupt.Err(); err != nil {
		return nil, nil, err
	}
	return ds, teardown, nil
}

// watchEngine kills the engine forwarder when the Engine VM exits during
// the session, e.g. because the agent powered it off as root inside it.
// QEMU's host ports are free then, and the forwarder would relay the agent
// to whatever binds them next, such as another project's Engine VM.
func watchEngine(vmDone, closing <-chan struct{}, ds *docker.Session) {
	select {
	case <-closing:
		return
	case <-vmDone:
	}
	fmt.Fprintln(os.Stderr, "\r\nWARNING: the Engine VM stopped; its route is closed for the rest of the session")
	if err := ds.Kill(sandbox.EngineService); err != nil {
		fmt.Fprintln(os.Stderr, "WARNING:", err)
	}
}

func printModelClashes(w io.Writer, model string, clashes []sessionregistry.Session) {
	for _, other := range clashes {
		fmt.Fprintf(w, "WARNING: model %s differs from %s in %s (%s, PID %d); llama-swap will swap models back and forth.\n",
			model, other.Model, other.Project, other.Agent, other.PID)
	}
}

// reportChanges warns about watched files, and the files in watch, that the
// session added, modified or deleted.
func reportChanges(w io.Writer, project string, watch []string, before changereport.Files) {
	after, err := changereport.SnapshotWith(project, watch)
	if err != nil {
		fmt.Fprintf(w, "WARNING: could not check for changed files that run things: %v\n", err)
		return
	}
	changes := changereport.Diff(before, after)
	if len(changes) == 0 {
		return
	}
	fmt.Fprintf(w, "WARNING: the session changed %d file(s) that can run code on the host. Review them before building, installing or opening the project in an editor:\n", len(changes))
	for _, line := range changereport.Warnings(changes) {
		fmt.Fprintln(w, "  "+line)
	}
}

func cmdCheck(args []string) error {
	sa, err := parseSessionArgs("check", args, nil)
	if err != nil {
		return err
	}
	sa.command = nil
	lease, err := lockProject(sa, "check")
	if err != nil {
		return err
	}
	defer releaseProject(lease)
	depPlan, err := sessionDeps(lease.paths.Project)
	if err != nil {
		return err
	}
	sess, err := prepareSession(sa, sandbox.ModeRun, lease.paths, depPlan)
	if err != nil {
		return err
	}
	defer sess.cleanup()

	printMounts(os.Stdout, sess.spec)
	printMasks(os.Stdout, sess.layout)
	sess.printWarnings(os.Stdout)

	ds, err := docker.Start(sess.spec, sess.sessionDir, sess.gatewayKey)
	if err != nil {
		return err
	}
	defer ds.Close()
	printEmptyDeps(os.Stdout, sess, ds.EmptyDeps())

	// A gateway problem is reported here and by the gateway probe; the
	// other probes still run.
	catalog, modelErr := sess.configure(ds, sa.model, false)
	printModels(os.Stdout, catalog, modelErr)

	start := time.Now()
	results, err := check.Run(ds, sess.gateway, sess.mounts)
	if err != nil {
		return fmt.Errorf("containment probes: %w", err)
	}
	fmt.Println("\nprobes:")
	printResults(os.Stdout, results)

	failed := probe.Failed(results)
	fmt.Printf("\n%d passed, %d failed (%s)\n", len(results)-len(failed), len(failed), time.Since(start).Round(10*time.Millisecond))
	if len(failed) > 0 {
		return errors.New("sandbox is not contained")
	}
	return modelErr
}

func cmdPrep(args []string) error {
	fset := flag.NewFlagSet("prep", flag.ContinueOnError)
	if err := fset.Parse(args); err != nil {
		return err
	}
	sa := sessionArgs{dir: "."}
	switch fset.NArg() {
	case 0:
	case 1:
		sa.dir = fset.Arg(0)
	default:
		return errors.New("prep takes at most one directory")
	}
	lease, err := lockProject(sa, "prep")
	if err != nil {
		return err
	}
	defer releaseProject(lease)
	project := lease.paths.Project
	depPlan, err := detectDeps(project)
	if err != nil {
		return err
	}
	engineCfg, err := engine.Read(project)
	if err != nil {
		return err
	}
	if len(depPlan.Steps) == 0 && engineCfg == nil {
		return fmt.Errorf("nothing to install in %s: no package.json or go.mod; set prep.command in %s", project, deps.ConfigFile)
	}
	if err := confirmPrep(project, depPlan, engineCfg); err != nil {
		return err
	}
	sess, err := prepareSession(sa, sandbox.ModePrep, lease.paths, depPlan)
	if err != nil {
		return err
	}
	before, err := changereport.SnapshotWith(sess.project, sess.layout.Watch)
	if err != nil {
		sess.cleanup()
		return fmt.Errorf("snapshot watched files: %w", err)
	}
	err = runPrep(sess, engineCfg)
	sess.cleanup()
	reportChanges(os.Stderr, sess.project, sess.layout.Watch, before)
	if err != nil {
		return err
	}
	// The next prep asks about changes made after this point.
	record, err := prepgate.Snapshot(sess.project)
	record.Dirs = sess.deps.Dirs
	if err == nil {
		err = record.Save(prepRecordFile(sess.project))
	}
	if err != nil {
		return fmt.Errorf("record prepped state: %w", err)
	}
	return nil
}

// cmdClean intentionally skips prepareSession: cleanup must work after a
// manifest/config was removed or broken and must not create mount placeholders.
func cmdClean(args []string) error {
	fset := flag.NewFlagSet("clean", flag.ContinueOnError)
	shared := fset.Bool("shared-caches", false, "also remove shared caches (requires all sessions to be idle)")
	var dirs []string
	for {
		if err := fset.Parse(args); err != nil {
			return err
		}
		if fset.NArg() == 0 {
			break
		}
		dirs = append(dirs, fset.Arg(0))
		args = fset.Args()[1:]
	}
	sa := sessionArgs{dir: ".", exclusiveCaches: *shared}
	switch len(dirs) {
	case 0:
	case 1:
		sa.dir = dirs[0]
	default:
		return errors.New("clean takes at most one directory")
	}
	lease, err := lockProject(sa, "clean")
	if err != nil {
		return err
	}
	defer releaseProject(lease)
	return cleanProject(lease.paths.Project, *shared, os.Stdin, os.Stderr)
}

func cleanProject(project string, shared bool, in io.Reader, out io.Writer) error {
	available, err := docker.ListVolumes()
	if err != nil {
		return err
	}
	volumes := sandbox.ProjectVolumes(project, available)
	if shared {
		for _, name := range sandbox.SharedCacheVolumes() {
			if slices.Contains(available, name) {
				volumes = append(volumes, name)
			}
		}
		slices.Sort(volumes)
	}
	record := prepRecordFile(project)
	_, err = os.Lstat(record)
	hasRecord := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect prep record: %w", err)
	}
	if len(volumes) == 0 && !hasRecord {
		fmt.Fprintf(out, "nothing to clean for %s\n", project)
		return nil
	}
	fmt.Fprintf(out, "Clean %s. Volumes to remove:\n", project)
	for _, name := range volumes {
		fmt.Fprintln(out, "  "+name)
	}
	if shared {
		fmt.Fprintln(out, "Shared caches are included; other projects will need to fetch dependencies again.")
	}
	if hasRecord {
		fmt.Fprintln(out, "The project's prep confirmation record will also be reset.")
	}
	fmt.Fprint(out, "Type y to clean: ")
	if !interrupt.Ask(func() bool { return readYes(in) }) {
		return errors.New("clean not run")
	}
	if err := docker.RemoveVolumes(volumes...); err != nil {
		return err
	}
	if err := os.Remove(record); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("reset prep record: %w", err)
	}
	fmt.Fprintln(out, "project volumes cleaned; run 'localllm prep' before the next session if dependencies are needed")
	return nil
}

// prepRecordFile is where the project's state after its last prep is kept,
// on the host and out of the sandbox's reach.
func prepRecordFile(project string) string {
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	return filepath.Join(cache, "localllm", "prep", sandbox.ProjectName(project)+".json")
}

// confirmPrep asks the user to type y before a prep whose inputs changed
// since the last prep, or before the first one: an agent session may have
// written what prep is about to run with network access.
func confirmPrep(project string, plan deps.Plan, engineCfg *engine.Config) error {
	d, err := prepDecision(project, plan)
	if err != nil {
		return err
	}
	if !d.Ask() {
		return nil
	}
	var images []string
	if engineCfg != nil {
		images = engineCfg.Images
	}
	printPrepGate(os.Stderr, project, plan, images, d)
	fmt.Fprint(os.Stderr, "Type y to run prep: ")
	if !interrupt.Ask(func() bool { return readYes(os.Stdin) }) {
		return errors.New("prep not run")
	}
	return nil
}

// prepDecision compares project with its last prep's record. Other changed
// files count when plan runs project code.
func prepDecision(project string, plan deps.Plan) (prepgate.Decision, error) {
	last, err := prepgate.Load(prepRecordFile(project))
	if err != nil {
		return prepgate.Decision{}, fmt.Errorf("read the last prep's record: %w", err)
	}
	now, err := prepgate.Snapshot(project)
	if err != nil {
		return prepgate.Decision{}, err
	}
	return prepgate.Decide(last, now, len(plan.Hooks) > 0), nil
}

func printPrepGate(w io.Writer, project string, plan deps.Plan, images []string, d prepgate.Decision) {
	if d.First {
		fmt.Fprintf(w, "First prep of %s. These run with network access:\n", project)
		for _, s := range plan.Steps {
			for _, c := range []string{s.Fetch, s.Script} {
				if c != "" {
					fmt.Fprintf(w, "  %s: %s\n", s.Name, c)
				}
			}
		}
		if len(images) > 0 {
			fmt.Fprintf(w, "  engine images, downloaded with crane: %s\n", strings.Join(images, ", "))
		}
	} else {
		fmt.Fprintf(w, "%s changed since its last prep, and prep runs with network access.\n", project)
		if len(d.Inputs) > 0 {
			fmt.Fprintln(w, "Files that decide what prep runs:")
			for _, line := range changereport.Warnings(d.Inputs) {
				fmt.Fprintln(w, "  "+line)
			}
		}
	}
	if len(plan.Hooks) > 0 {
		fmt.Fprintln(w, "The install runs project code:")
		for _, h := range plan.Hooks {
			fmt.Fprintln(w, "  "+h)
		}
	}
	if len(d.Others) > 0 {
		const max = 20
		fmt.Fprintf(w, "%d other file(s) changed since the last prep, which that code may run:\n", len(d.Others))
		for i, line := range changereport.Warnings(d.Others) {
			if i == max {
				fmt.Fprintf(w, "  ... and %d more\n", len(d.Others)-max)
				break
			}
			fmt.Fprintln(w, "  "+line)
		}
	}
	if !d.First {
		fmt.Fprintf(w, "Review them first, e.g. git -C %s diff\n", project)
	}
}

// readYes reads one line and reports whether it is y or yes.
func readYes(r io.Reader) bool {
	line, _ := bufio.NewReader(r).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// runPrep runs the install steps with network access, without an agent,
// the gateway or its key, and downloads engineCfg's images if set. The
// containers are gone when it returns.
func runPrep(sess *session, engineCfg *engine.Config) error {
	sess.printWarnings(os.Stderr)
	if n := len(sess.mounts.Masked); n > 0 {
		fmt.Fprintf(os.Stderr, "masked %d secret path(s); install scripts can't read them\n", n)
	}
	for _, d := range sess.deps.Dirs {
		fmt.Fprintf(os.Stderr, "dependency volume: %s\n", d.Path)
	}
	// Fresh volumes, so nothing an agent session left in them runs here.
	var vols []string
	for _, d := range sess.deps.Dirs {
		vols = append(vols, sandbox.DepVolume(sess.project, d.Path))
	}
	if err := docker.RemoveVolumes(vols...); err != nil {
		return fmt.Errorf("%w (is a session for this project running?)", err)
	}
	if sess.deps.FetchScript() != "" {
		fmt.Fprintln(os.Stderr, "fetching into the shared caches (no project code runs)")
		if err := runAttached(sess.fetchSpec, sess.sessionDir); err != nil {
			return fmt.Errorf("prep fetch failed: %w", err)
		}
	}
	if err := interrupt.Err(); err != nil {
		return err
	}
	if engineCfg != nil {
		if err := fetchEngineImages(sess.project, engineCfg); err != nil {
			return err
		}
	}
	if err := interrupt.Err(); err != nil {
		return err
	}
	if sess.deps.Script() != "" {
		fmt.Fprintln(os.Stderr, "installing (shared caches read-only)")
		if err := runAttached(sess.spec, sess.sessionDir); err != nil {
			return fmt.Errorf("prep install failed: %w", err)
		}
	}
	fmt.Fprintln(os.Stderr, "dependencies installed; 'localllm run' uses them offline")
	return nil
}

// fetchEngineImages downloads the project's Engine images into its archive
// cache. crane runs in a throwaway container of the base image that has
// only the new archive directory mounted: no project files, so no project
// code. Each image is pulled by the digest its tag resolves to. The
// previous archives are replaced only after every download succeeded.
func fetchEngineImages(project string, cfg *engine.Config) error {
	layout, err := engineLayout()
	if err != nil {
		return err
	}
	m, err := engine.MachineFor(engineHost())
	if err != nil {
		return err
	}
	dir := layout.ArchiveDir(project)
	staging := dir + ".new"
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	fmt.Fprintf(os.Stderr, "downloading %d engine image(s) for %s (no project code runs)\n", len(cfg.Images), m.Platform)
	if err := docker.RunOnline(baseImage, os.Getuid(), os.Getgid(), staging, "sh", "-c", engine.FetchScript(cfg.Images, m.Platform)); err != nil {
		return fmt.Errorf("download engine images: %w", err)
	}
	// The directory was written by a container: take only regular files,
	// so a symlink can't point the host at its own files.
	if err := regularFile(filepath.Join(staging, engine.DigestsFile)); err != nil {
		return fmt.Errorf("download engine images: %w", err)
	}
	digests, err := os.ReadFile(filepath.Join(staging, engine.DigestsFile))
	if err != nil {
		return fmt.Errorf("download engine images: %w", err)
	}
	rec, err := engine.ParseDigests(cfg.Images, string(digests))
	if err != nil {
		return err
	}
	for _, a := range rec.Archives {
		if err := regularFile(filepath.Join(staging, a.File)); err != nil {
			return fmt.Errorf("download engine image %s: %w", a.Ref, err)
		}
	}
	if err := os.Remove(filepath.Join(staging, engine.DigestsFile)); err != nil {
		return err
	}
	if err := rec.Save(staging); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Rename(staging, dir)
}

// runAttached starts spec and runs its agent service attached to the
// terminal. The containers are gone when it returns.
func runAttached(spec sandbox.Compose, sessionDir string) error {
	ds, err := docker.Start(spec, sessionDir, "")
	if err != nil {
		return err
	}
	defer ds.Close()
	return ds.Interactive()
}

// regularFile returns an error unless file is a non-empty regular file,
// not a symlink.
func regularFile(file string) error {
	info, err := os.Lstat(file)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("%s is not a non-empty regular file", file)
	}
	return nil
}

// printEmptyDeps hints at prep for each dependency volume that is empty.
func printEmptyDeps(w io.Writer, sess *session, empty []string) {
	for _, d := range sess.deps.Dirs {
		if slices.Contains(empty, sandbox.Workspace+"/"+d.Path) {
			fmt.Fprintf(w, "hint: %s is empty although %s exists; run 'localllm prep %s' to install dependencies\n", d.Path, d.Manifest, sess.project)
		}
	}
}

// configure resolves the session's models from the gateway, through the
// started sandbox, and writes the agent config. No agent container exists
// yet, so the config is complete before the agent first sees it.
func (s *session) configure(ds *docker.Session, model string, auto bool) (models.Catalog, error) {
	file, err := models.ParseFile(assets.Models)
	if err != nil {
		return models.Catalog{}, err
	}
	data, err := ds.GatewayGet("/v1/models")
	if errors.Is(err, docker.ErrUnauthorized) {
		if s.gatewayKey == "" {
			return models.Catalog{}, fmt.Errorf("%w; no key is configured: %s", err, gatewaykey.Hint)
		}
		return models.Catalog{}, fmt.Errorf("%w; check the key (%s)", err, gatewaykey.Hint)
	}
	if err != nil {
		return models.Catalog{}, err
	}
	discovered, err := models.ParseDiscovered(data)
	if err != nil {
		return models.Catalog{}, err
	}
	catalog, err := models.Resolve(discovered, file, model)
	if err != nil {
		return models.Catalog{}, err
	}
	keyEnv := ""
	if s.gatewayKey != "" {
		keyEnv = sandbox.GatewayKeyEnv
	}
	contents, err := s.agent.render(catalog, auto, keyEnv)
	if err != nil {
		return models.Catalog{}, err
	}
	for i, file := range s.cfgFiles {
		if err := os.WriteFile(file, contents[i], 0o644); err != nil {
			return models.Catalog{}, err
		}
	}
	return catalog, nil
}

// confirmFullAuto asks the user to type y before a full-auto session on a
// project with uncommitted changes. It returns an error when they decline.
func confirmFullAuto(project, agent string, auto bool) error {
	if !approval.FullAuto(agent, auto) {
		return nil
	}
	status, err := approval.ReadStatus(project, offlineRunner)
	if err != nil {
		return err
	}
	if !approval.NeedsConfirmation(true, status) {
		return nil
	}
	if !interrupt.Ask(func() bool { return approval.Confirm(os.Stdin, os.Stderr, agent, status) }) {
		msg := "not started; commit or stash your changes first (git stash -u)"
		if !approval.FullAuto(agent, false) {
			msg += ", or run without --auto"
		}
		return errors.New(msg)
	}
	return nil
}

// offlineRunner runs git for approval.ReadStatus in the base image, offline
// and read-only, so filters or an fsmonitor the agent configured never run
// on the host. Paths outside the project, such as a linked worktree's git
// dir, must pass the --ro path policy.
func offlineRunner(mounts []string, dir string, cmd ...string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	policy := pathpolicy.Default(home, os.Getenv("LOCALLLM_ALLOWED_ROOT"))
	for _, m := range mounts {
		if err := policy.CheckReadOnly(m); err != nil {
			return "", err
		}
	}
	return docker.RunOffline(baseImage, os.Getuid(), os.Getgid(), mounts, dir, cmd...)
}

func printModels(w io.Writer, c models.Catalog, err error) {
	fmt.Fprintln(w, "models:")
	if err != nil {
		fmt.Fprintf(w, "  error: %v\n\n", err)
		return
	}
	for _, m := range c.Models {
		var tags []string
		if m.ID == c.Default {
			tags = append(tags, "default")
		}
		if m.ID == c.Small {
			tags = append(tags, "small")
		}
		if !m.Tools {
			tags = append(tags, "no tools")
		}
		label := ""
		if len(tags) > 0 {
			label = " (" + strings.Join(tags, ", ") + ")"
		}
		fmt.Fprintf(w, "  %s  context %d, output %d%s\n", m.ID, m.Context, m.Output, label)
	}
	for _, n := range c.Notes {
		fmt.Fprintln(w, "  note:", n)
	}
	fmt.Fprintln(w)
}

func printMounts(w io.Writer, spec sandbox.Compose) {
	fmt.Fprintf(w, "image: %s\n", spec.Services[sandbox.AgentService].Image)
	fmt.Fprintln(w, "mounts:")
	for _, v := range spec.Services[sandbox.AgentService].Volumes {
		mode := "rw"
		if v.ReadOnly {
			mode = "ro"
		}
		source := v.Source
		if v.Type == "volume" {
			source = "volume " + spec.Volumes[v.Source].Name
		}
		fmt.Fprintf(w, "  %s  %s -> %s\n", mode, source, v.Target)
	}
	fmt.Fprintf(w, "gateway (in sandbox): %s\n", sandbox.AgentGatewayURL())
	key := "none"
	if slices.Contains(spec.Services[sandbox.AgentService].PassEnv, sandbox.GatewayKeyEnv) {
		key = "configured (value not shown)"
	}
	fmt.Fprintf(w, "gateway key: %s\n", key)
}

func printMasks(w io.Writer, layout mask.Layout) {
	fmt.Fprintf(w, "\nmasks (computed at launch; files created later are not masked; rules: defaults + %s):\n", mask.IgnoreFile)
	n := 0
	for _, m := range layout.Mounts {
		if !m.Mask {
			continue
		}
		n++
		if m.Dir {
			fmt.Fprintf(w, "  %s/ (directory, shown empty)\n", m.Path)
		} else {
			fmt.Fprintf(w, "  %s (shown empty)\n", m.Path)
		}
	}
	if n == 0 {
		fmt.Fprintln(w, "  none")
	}
	fmt.Fprintln(w)
}

func printResults(w io.Writer, results []probe.Result) {
	for _, r := range results {
		status := "PASS"
		if !r.Passed() {
			status = "FAIL"
		}
		want := "must fail"
		if r.MustSucceed {
			want = "must succeed"
		}
		outcome := fmt.Sprintf("exit %d", r.ExitCode)
		if r.Missing {
			outcome = "no result"
		}
		fmt.Fprintf(w, "  %s  %-34s %-13s %s\n", status, r.Name, want, outcome)
	}
}

// newSessionDir creates a per-session directory under the user cache dir,
// which lives under $HOME and is therefore shared into the Docker VM.
func newSessionDir(project string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(cache, "localllm", "sessions")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(root, sandbox.ProjectName(project)+"-"+strconv.Itoa(os.Getpid())+"-")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

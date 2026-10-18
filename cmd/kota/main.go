// Command kota runs coding agents in an offline Docker sandbox that can
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
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"kota/assets"
	"kota/internal/agent"
	"kota/internal/approval"
	"kota/internal/changereport"
	"kota/internal/check"
	"kota/internal/deps"
	"kota/internal/docker"
	"kota/internal/engine"
	"kota/internal/engineimages"
	"kota/internal/enginesession"
	"kota/internal/enginevm"
	"kota/internal/gatewaykey"
	"kota/internal/hostdata"
	"kota/internal/instructions"
	"kota/internal/interrupt"
	"kota/internal/lifecycle"
	"kota/internal/mask"
	"kota/internal/models"
	"kota/internal/nohistory"
	"kota/internal/pathpolicy"
	"kota/internal/prepgate"
	"kota/internal/probe"
	"kota/internal/projectimage"
	"kota/internal/sandbox"
	"kota/internal/sessionregistry"
)

const (
	baseImage      = "kota-base:latest"
	forwarderImage = "kota-forwarder:latest"
	defaultGateway = "http://host.docker.internal:8686"
	defaultAgent   = agent.Default
	// engineHostAlias is the host as the engine forwarder reaches it, where
	// QEMU's port forwards listen on 127.0.0.1.
	engineHostAlias = "host.docker.internal"
	// engineBootTimeout bounds the Engine VM's boot to a ready Docker API.
	engineBootTimeout = 60 * time.Second
)

const usage = `usage: kota <command> [flags]

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
  check [flags] [dir]   show mounts and the Engine VM, and run the containment
                        probes
  clean [flags] [dir]   remove project state and dependency volumes after
                        confirmation
  instructions          print the working style the agent is told to follow,
                        and say whether it is the built-in default or your
                        override

flags:
  --agent NAME          opencode (default) or pi; pi has no permission
                        system and always runs in full auto
  --gateway URL         model gateway (default $KOTA_GATEWAY or ` + defaultGateway + `)
  --model ID            model to start with (default: models.yaml's default);
                        the agent is offered every model the gateway serves
  --ro PATH             mount PATH read-only at ` + sandbox.ReadOnlyRoot + `/<name> (repeatable)
  --auto                run only: opencode approves edits and commands without
                        asking; on uncommitted changes, asks you to type y
  --unsafe-skip-check   run only: start without the containment probes
  --no-engine           run and shell: start without the project's Engine VM;
                        the agent is told tests needing containers can't run
  --no-history          run and shell: run on a copy that keeps only the last
                        commit (its files, message, author and date), with no
                        older commits, remotes or git config; masked files are
                        still readable from that commit; its new commits come
                        back as the branch kota/<agent>-<time>
  --shared-caches       clean only: also reset shared caches; all sessions
                        must be idle

dir must be inside the allowed root ($KOTA_ALLOWED_ROOT, default ~/Projects).
$XDG_CONFIG_HOME/kota/instructions.md (default ~/.config/kota/instructions.md),
when present, replaces the built-in working style for every project.
$XDG_CONFIG_HOME/kota/opencode/cli.json, when present, is merged over
opencode's bundled terminal settings in every session; it may set keybinds,
leader, theme, animations, cursor, mouse, scroll, prompt, session, tabs,
diffs, terminal, mini and highlights. $XDG_CONFIG_HOME/kota/opencode/opencode.json
is merged over opencode's bundled config; it may set compaction (keybinds
and theme go in cli.json). $XDG_CONFIG_HOME/kota/pi/settings.json
is merged over pi's bundled settings; it may set defaultThinkingLevel,
modelThinkingLevels, thinkingBudgets, hideThinkingBlock, steeringMode,
followUpMode, doubleEscapeAction, treeFilterMode, compaction, branchSummary,
retry, theme, quietStartup, tuiMode, fullscreenExitOutput,
fullscreenScrollbar, fullscreenCopyOnSelect, fullscreenWheelScrollLines,
editorPaddingX, outputPad, autocompleteMaxVisible, showHardwareCursor,
terminal, images and markdown. $XDG_CONFIG_HOME/kota/pi/keybindings.json
rebinds any of pi's actions.
A gateway API key (llama-swap apiKeys) is read from $KOTA_GATEWAY_KEY or
the file named by $KOTA_GATEWAY_KEY_FILE.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	// A signal stops kota at the next step, after which it tears down as
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
	case "instructions":
		err = cmdInstructions(os.Args[2:], os.Stdout, os.Stderr)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if interrupt.Err() != nil {
		// Whatever failed, it failed because of the signal.
		fmt.Fprintln(os.Stderr, "kota: interrupted")
		os.Exit(130)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "kota:", err)
		os.Exit(1)
	}
}

// cmdInstructions prints the working style in effect to out, so that
// 'kota instructions > ~/.config/kota/instructions.md' starts an override,
// and its source to info.
func cmdInstructions(args []string, out, info io.Writer) error {
	fset := flag.NewFlagSet("instructions", flag.ContinueOnError)
	if err := fset.Parse(args); err != nil {
		return err
	}
	if fset.NArg() > 0 {
		return errors.New("instructions takes no arguments")
	}
	style, err := loadStyle()
	if err != nil {
		return err
	}
	fmt.Fprintln(info, "instructions:", style.Source)
	_, err = io.WriteString(out, style.Text)
	return err
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
// project's image archives, in the user cache dir with kota's other
// host state, outside every project and sandbox.
func engineLayout() (engine.Layout, error) {
	dirs, err := hostdata.Default()
	return dirs.Engine(), err
}

func engineHost() engine.Host { return engine.Host{OS: runtime.GOOS, Arch: runtime.GOARCH} }

// buildProject builds a project's image from its Dockerfile on top of the
// base image.
func buildProject(dir string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	paths, err := pathpolicy.Validate(dir, nil, pathpolicy.Default(home, os.Getenv("KOTA_ALLOWED_ROOT")))
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
		return fmt.Errorf("the base image %s is missing; run 'kota build' first", baseImage)
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

// session holds everything needed to start one sandbox.
type session struct {
	shell bool
	spec  sandbox.Compose
	// fetchSpec is prep's fetch phase, which fills the shared caches before
	// spec installs.
	fetchSpec  sandbox.Compose
	gateway    sandbox.Gateway
	sessionDir string
	// project is the directory mounted at the workspace.
	project string
	// key is the project the session belongs to, which its volumes, prep,
	// image, Engine VM and lease are keyed by: project, or the original
	// project of a No-history copy.
	key    string
	layout mask.Layout
	// agent is the session's agent with its Agent overrides, and prepared
	// its files in the session dir; both nil for prep.
	agent    *agent.Agent
	prepared *agent.Prepared
	// gatewayKey is the gateway API key, empty when none is configured. It
	// only reaches the sandbox through docker compose's environment.
	gatewayKey string
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
	engine *enginesession.Engine
	// engineErr is why the Engine VM can't start, with
	// sessionArgs.engineOptional.
	engineErr error
	// engineOff is set when the project has an Engine VM, but the session
	// runs without it (--no-engine).
	engineOff bool
	// style is the working style the agent's note starts with; unset for
	// prep.
	style instructions.Style
}

// loadStyle reads the user's working style override, or returns the
// built-in one.
func loadStyle() (instructions.Style, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return instructions.Style{}, err
	}
	builtin, err := assets.Agents.ReadFile(instructions.Default)
	if err != nil {
		return instructions.Style{}, err
	}
	return instructions.Load(instructions.OverridePath(os.Getenv, home), builtin)
}

// loadAgent picks the agent called name and reads the user's Agent
// overrides of it.
func loadAgent(name string) (*agent.Agent, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return agent.Load(name, instructions.ConfigHome(os.Getenv, home))
}

// overrideSource says which Agent override files the session uses.
func overrideSource(files []string) string {
	if len(files) == 0 {
		return "none (bundled config)"
	}
	return strings.Join(files, ", ")
}

// engineNote is the agent's instruction about the Engine VM: none for a
// project without one.
func (s *session) engineNote() string {
	switch {
	case s.engine != nil:
		return sandbox.EngineNote(true, s.engine.Images())
	case s.engineOff:
		return sandbox.EngineNote(false, nil)
	}
	return ""
}

// engineOptions are what this kota's Engine VMs share.
func engineOptions() (enginesession.Options, error) {
	layout, err := engineLayout()
	if err != nil {
		return enginesession.Options{}, err
	}
	return enginesession.Options{Layout: layout, Host: engineHost(), ProbeSource: forwarderImage,
		HostAlias: engineHostAlias, BootTimeout: engineBootTimeout}, nil
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
	if s.engine != nil && s.engine.Warning() != "" {
		fmt.Fprintln(w, "WARNING:", s.engine.Warning())
	}
	if s.engineOff {
		told := "the agent is told to report them as not run"
		if s.shell {
			told = sandbox.EngineEnv + "=" + sandbox.EngineUnavailable
		}
		fmt.Fprintf(w, "note: --no-engine: the Engine VM is off, so tests that need containers can't run (%s)\n", told)
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
	// engine starts the project's Engine VM, if it declares one. Without
	// it, a run session of such a project tells the agent the VM is off.
	engine bool
	// engineOptional keeps the session going when the Engine VM can't be
	// set up, recording why in session.engineErr, so check can report it.
	engineOptional bool
	// noHistory runs the session on a No-history copy of the project.
	noHistory  bool
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
	gw := fset.String("gateway", envOr("KOTA_GATEWAY", defaultGateway), "model gateway URL")
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
	return pathpolicy.Validate(sa.dir, sa.ro, pathpolicy.Default(home, os.Getenv("KOTA_ALLOWED_ROOT")))
}

// detectDeps works out the project's dependency plan. It only reads the
// project, so it can run before the user is asked anything.
func detectDeps(project string) (deps.Plan, error) {
	return deps.Detect(project, deps.Options{PnpmStore: sandbox.PnpmStore})
}

// sessionDeps is detectDeps for run, shell and check. When detection fails,
// e.g. on a manifest an agent left broken, the session still starts so the
// file can be fixed, with the fallback volumes of deps.Fallback. Only prep
// refuses. key is the project the session belongs to (see session.key).
func sessionDeps(project, key string) (deps.Plan, error) {
	plan, err := detectDeps(project)
	if err == nil {
		return plan, nil
	}
	file, lerr := prepRecordFile(key)
	var last *prepgate.Record
	if lerr == nil {
		last, lerr = prepgate.Load(file)
	}
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
// first. The agent writes its files in sessionDir/agent; its config is
// filled in later by configure. The caller removes sessionDir when done.
// Prep has no agent, gateway or key and runs the dependency install. key is
// the project the session belongs to: paths.Project, or the original
// project of a No-history copy.
func prepareSession(sa sessionArgs, mode sandbox.Mode, paths pathpolicy.Paths, key string, depPlan deps.Plan) (*session, error) {
	prep := mode == sandbox.ModePrep
	var ag *agent.Agent
	var style instructions.Style
	var err error
	if !prep {
		if ag, err = loadAgent(sa.agent); err != nil {
			return nil, err
		}
		if style, err = loadStyle(); err != nil {
			return nil, err
		}
	}
	project := paths.Project
	var gw sandbox.Gateway
	var apiKey string
	if !prep {
		if gw, err = sandbox.ParseGateway(sa.gatewayURL); err != nil {
			return nil, err
		}
		if apiKey, err = gatewaykey.Load(os.Getenv); err != nil {
			return nil, err
		}
	}
	layout, err := mask.Plan(project, mask.DefaultRules)
	if err != nil {
		return nil, err
	}
	// The copy's kota.Dockerfile is the original's (nohistory.TakeConfig),
	// and the image is the original's.
	image, err := chooseImage(key)
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

	sessionDir, err := newSessionDir(key)
	if err != nil {
		return nil, err
	}
	sess := &session{shell: sa.shell, gateway: gw, sessionDir: sessionDir, project: project, key: key, layout: layout, agent: ag, gatewayKey: apiKey, deps: depPlan, image: image, style: style}
	var engineRoute *sandbox.EngineRoute
	if !prep {
		opts, err := engineOptions()
		if err == nil && sa.engine {
			sess.engine, err = enginesession.Prepare(opts, key, sessionDir)
			if err != nil && sa.engineOptional {
				sess.engine, sess.engineErr, err = nil, err, nil
			}
		} else if err == nil {
			sess.engineOff = enginesession.Declared(opts.Layout, key)
		}
		if err != nil {
			os.RemoveAll(sessionDir)
			return nil, err
		}
		if sess.engine != nil {
			engineRoute = sess.engine.Route()
		}
	}
	// Prep runs no agent.
	var cfgMounts []sandbox.Mount
	var scratch []string
	var command []string
	env := map[string]string{}
	if !prep {
		p, err := ag.Prepare(filepath.Join(sessionDir, "agent"), agent.Note{Style: style, Engine: sess.engineNote()})
		if err != nil {
			os.RemoveAll(sessionDir)
			return nil, err
		}
		sess.prepared = p
		cfgMounts = slices.Clone(p.ReadOnly)
		scratch, command = p.Scratch, p.Command
		maps.Copy(env, p.Env)
	}
	var depMounts []sandbox.Mount
	for _, d := range depPlan.Dirs {
		sess.mountPoints = append(sess.mountPoints, missingDirs(project, d.Path)...)
		depMounts = append(depMounts, sandbox.Mount{Source: sandbox.DepVolume(key, d.Path), Target: sandbox.Workspace + "/" + d.Path})
	}
	pinned, protectedMounts, err := layout.Bind(project, sessionDir)
	if err == nil {
		err = layout.Prepare(project)
	}
	excludes := filepath.Join(sessionDir, "git-excludes")
	if err == nil {
		err = os.WriteFile(excludes, []byte(layout.Excludes()), 0o444)
	}
	if err != nil {
		sess.cleanup()
		return nil, err
	}
	cfgMounts = append(cfgMounts, sandbox.Mount{Source: excludes, Target: sandbox.GitExcludes})
	sess.mounts.Pinned, sess.mounts.ReadOnly, sess.mounts.Masked = layout.Targets()
	if sess.prepared != nil {
		for _, m := range sess.prepared.ReadOnly {
			sess.mounts.ReadOnly = append(sess.mounts.ReadOnly, m.Target)
		}
	}
	sess.mounts.ReadOnly = append(sess.mounts.ReadOnly, sandbox.GitExcludes)
	if !prep {
		sess.mounts.State = sandbox.StateDir
	}
	for _, m := range paths.ReadOnly {
		sess.mounts.ReadOnly = append(sess.mounts.ReadOnly, m.Target)
	}
	if engineRoute != nil {
		sess.mounts.ReadOnly = append(sess.mounts.ReadOnly, sandbox.EngineCertDir)
	}

	maps.Copy(env, gitEnv(key))
	if len(sa.command) > 0 {
		command = sa.command
	}
	if sa.shell {
		command = []string{"bash", "--noprofile", "--norc", "-i"}
	}
	stateVolume := ""
	if prep {
		command = []string{"sh", "-c", depPlan.Script()}
	} else {
		state := ag.Name()
		if sa.noHistory {
			state += sandbox.NoHistoryState
		}
		stateVolume = sandbox.StateVolume(key, state)
	}
	in := sandbox.Inputs{
		ProjectDir:     project,
		Key:            key,
		UID:            os.Getuid(),
		GID:            os.Getgid(),
		Mode:           mode,
		Gateway:        gw,
		GatewayKey:     apiKey != "",
		AgentImage:     image.Image,
		ForwarderImage: forwarderImage,
		HelperImage:    baseImage,
		Pinned:         pinned,
		ReadOnly:       append(append(cfgMounts, protectedMounts...), paths.ReadOnly...),
		Scratch:        scratch,
		StateVolume:    stateVolume,
		Deps:           depMounts,
		Env:            env,
		Command:        command,
		Engine:         engineRoute,
		EngineOff:      sess.engineOff,
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
// trusts for safe.directory. The excludes file keeps placeholders and masks
// out of git add; the host's own global excludes aren't passed in.
func gitEnv(project string) map[string]string {
	settings := [][2]string{{"safe.directory", "*"}, {"core.excludesFile", sandbox.GitExcludes}}
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

// parseOfflineArgs is parseSessionArgs for run and shell, which start the
// project's Engine VM unless --no-engine.
func parseOfflineArgs(name string, args []string, register func(*flag.FlagSet)) (sessionArgs, error) {
	var noEngine, noHistory bool
	sa, err := parseSessionArgs(name, args, func(f *flag.FlagSet) {
		f.BoolVar(&noEngine, "no-engine", false, "start without the project's Engine VM")
		f.BoolVar(&noHistory, "no-history", false, "run on a copy that keeps only the last commit, without older history")
		if register != nil {
			register(f)
		}
	})
	sa.engine = !noEngine
	sa.noHistory = noHistory
	return sa, err
}

func cmdRun(args []string) error {
	var skipCheck, auto bool
	sa, err := parseOfflineArgs("run", args, func(f *flag.FlagSet) {
		f.BoolVar(&skipCheck, "unsafe-skip-check", false, "start without running the containment probes")
		f.BoolVar(&auto, "auto", false, "approve edits and commands without asking")
	})
	if err != nil {
		return err
	}
	return cmdOffline(sa, auto, skipCheck)
}

func cmdShell(args []string) error {
	sa, err := parseOfflineArgs("shell", args, nil)
	if err != nil {
		return err
	}
	if len(sa.command) > 0 {
		return errors.New("shell does not take a command; use 'kota run [dir] -- cmd...' instead")
	}
	sa.shell = true
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
	dirs, err := hostdata.Default()
	if err != nil {
		return nil, err
	}
	r := &sessionregistry.Registry{
		Dir: dirs.Registry(),
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
	// Fail on an unknown agent, a refused Agent override or a broken
	// models.yaml before asking.
	ag, err := loadAgent(sa.agent)
	if err != nil {
		return err
	}
	if _, err := models.ParseFile(assets.Models); err != nil {
		return err
	}
	start := time.Now()
	paths, key, err := offlinePaths(sa, lease, ag, auto, start)
	if err != nil {
		return err
	}
	// discard removes the copy while no Session has used it yet.
	discard := func() {
		if sa.noHistory {
			os.RemoveAll(paths.Project)
		}
	}
	depPlan, err := sessionDeps(paths.Project, key)
	if err != nil {
		discard()
		return err
	}
	sess, err := prepareSession(sa, sandbox.ModeRun, paths, key, depPlan)
	if err != nil {
		discard()
		return err
	}
	// Snapshot before Docker creates any mount points, and compare after
	// cleanup has removed them, so placeholders aren't reported.
	before, err := changereport.SnapshotWith(sess.project, sess.layout.Watch)
	if err != nil {
		sess.cleanup()
		discard()
		return fmt.Errorf("snapshot watched files: %w", err)
	}
	commits := startCommitCheck(os.Stderr, sess.project)
	err = runSession(sess, lease.Lease, sa.model, auto, skipCheck)
	commits.finish()
	if !sa.noHistory {
		sess.cleanup()
		reported := reportChanges(os.Stderr, sess.project, sess.layout.Watch, before)
		commits.report(os.Stderr, sess.layout, reported)
		return err
	}
	if commits != nil {
		commits.where = " in the copy"
	}
	// Before cleanup, as commits.finish: the copy's status and export read
	// its .git, where cleanup removes seeded files.
	ret := returnWork(sess, commits, returnName(sa, ag.Name()), start)
	sess.cleanup()
	// The copy's working tree is discarded, so watched files changed in
	// its commits are always flagged, and its changes are listed in full
	// when it is kept.
	commits.report(os.Stderr, sess.layout, nil)
	ret.report(os.Stderr)
	return err
}

// returnName names a Session's Returned branch: the agent, or "shell".
func returnName(sa sessionArgs, agent string) string {
	if sa.shell {
		return "shell"
	}
	return agent
}

// returned is what returnWork did with a No-history copy.
type returned struct {
	copy string
	// branch is the Returned branch, when one was made.
	branch nohistory.Returned
	// none is why no branch was made, when none was.
	none string
	// unreturned are branches and tags in the copy that weren't brought
	// back, uncommitted its uncommitted changes.
	unreturned, uncommitted []string
	// keep are the reasons the copy is kept; it is deleted when there are
	// none.
	keep []string
}

// returnWork brings the copy's HEAD back into the original as the
// Returned branch, and deletes the copy unless something in it would be
// lost. Call it once the containers are gone, before the session's
// cleanup. git runs in offline containers only: in the copy only with its
// bundle's directory writable, and in the original without the copy.
func returnWork(sess *session, commits *commitCheck, name string, start time.Time) returned {
	r := returned{copy: sess.project}
	if commits == nil || commits.err != nil {
		r.none = "could not read the copy's commits"
		r.keep = append(r.keep, r.none)
		return r
	}
	uncommitted, err := nohistory.Uncommitted(sess.project, filepath.Join(sess.sessionDir, "git-excludes"), offlineRunner)
	if err != nil {
		r.keep = append(r.keep, fmt.Sprintf("could not read its status: %v", err))
	} else if r.uncommitted = uncommitted; len(uncommitted) > 0 {
		r.keep = append(r.keep, "uncommitted changes")
	}
	if r.unreturned = nohistory.Unreturned(commits.before, commits.after); len(r.unreturned) > 0 {
		r.keep = append(r.keep, "branches or tags not brought back")
	}
	base, head := commits.before.Head, commits.after.Head
	switch {
	case head == base:
		r.none = "no commits to bring back"
	case head == "":
		r.none = "the copy's HEAD has no commit"
		r.keep = append(r.keep, r.none)
	default:
		r.branch, err = bringBack(sess, base, nohistory.BranchName(name, start))
		if errors.Is(err, nohistory.ErrRewritten) {
			r.none = err.Error()
		} else if err != nil {
			r.none = fmt.Sprintf("could not bring back: %v", err)
		}
		if err != nil {
			r.keep = append(r.keep, "its commits weren't brought back")
		}
	}
	if len(r.keep) == 0 {
		if err := os.RemoveAll(sess.project); err != nil {
			r.keep = append(r.keep, fmt.Sprintf("could not remove it: %v", err))
		}
	}
	return r
}

// bringBack exports the copy's new commits to a bundle in the session dir
// and imports them into the original as branch.
func bringBack(sess *session, base, branch string) (nohistory.Returned, error) {
	dir, err := os.MkdirTemp(sess.sessionDir, "return-")
	if err != nil {
		return nohistory.Returned{}, err
	}
	bundle := filepath.Join(dir, "return.bundle")
	if err := nohistory.Export(sess.project, base, bundle, writableRunner); err != nil {
		return nohistory.Returned{}, err
	}
	return nohistory.Import(sess.key, bundle, base, branch, writableRunner)
}

// report prints the return part of the change report.
func (r returned) report(w io.Writer) {
	if r.branch.Branch != "" {
		fmt.Fprintf(w, "returned branch %s (%d commit(s), at %s); review it, then merge it yourself\n",
			strings.TrimPrefix(r.branch.Branch, "refs/heads/"), r.branch.Commits, r.branch.Commit[:min(12, len(r.branch.Commit))])
	} else {
		fmt.Fprintf(w, "no-history: no branch brought back: %s\n", r.none)
	}
	if len(r.unreturned) > 0 {
		fmt.Fprintf(w, "not brought back: %s\n", strings.Join(r.unreturned, ", "))
	}
	if len(r.keep) == 0 {
		fmt.Fprintln(w, "the copy was removed")
		return
	}
	fmt.Fprintf(w, "copy kept at %s (%s)\n", r.copy, strings.Join(r.keep, "; "))
	for i, line := range r.uncommitted {
		if i == maxListedChanges {
			fmt.Fprintf(w, "  ... and %d more\n", len(r.uncommitted)-maxListedChanges)
			break
		}
		fmt.Fprintln(w, "  "+line)
	}
	fmt.Fprintln(w, "  git in the copy reads config and hooks the agent could write: review them before running git there; kota clean removes the copy")
}

// maxListedChanges is how many uncommitted changes in a kept copy the
// report lists.
const maxListedChanges = 20

// offlinePaths returns the paths a run or shell session mounts and the
// project it belongs to, after the full-auto prompt or, with --no-history,
// after making the No-history copy.
func offlinePaths(sa sessionArgs, lease *lockedProject, ag *agent.Agent, auto bool, start time.Time) (pathpolicy.Paths, string, error) {
	paths, key := lease.paths, lease.paths.Project
	if sa.noHistory {
		// The agent works on the copy, so the original's uncommitted
		// changes are out of its reach, and the full-auto prompt has
		// nothing to protect.
		var err error
		paths.Project, err = makeNoHistoryCopy(key, start)
		return paths, key, err
	}
	if !sa.shell {
		if err := confirmFullAuto(key, ag.Name(), ag.FullAuto(auto), ag.FullAuto(false)); err != nil {
			return paths, key, err
		}
	}
	return paths, key, nil
}

// makeNoHistoryCopy checks that project can be copied, asks about what
// the copy leaves out, and makes the No-history copy. It returns the copy's
// validated path. start names it, as it names the Returned branch. Call it
// under project's lease.
func makeNoHistoryCopy(project string, start time.Time) (string, error) {
	missing, err := nohistory.Inspect(project, offlineRunner)
	if err != nil {
		return "", err
	}
	if !missing.Empty() && !interrupt.Ask(func() bool { return nohistory.Confirm(os.Stdin, os.Stderr, missing) }) {
		return "", errors.New("not started; commit or stash your changes first (git stash -u), or run without --no-history")
	}
	dirs, err := hostdata.Default()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dirs.Copies(), 0o700); err != nil {
		return "", err
	}
	dest := dirs.CopyDir(project, start)
	if _, err := os.Lstat(dest); err == nil {
		return "", fmt.Errorf("%s already exists; start again in a second", dest)
	}
	copied, err := func() (string, error) {
		if err := nohistory.Clone(project, dest, writableRunner); err != nil {
			return "", err
		}
		if err := nohistory.TakeConfig(project, dest); err != nil {
			return "", err
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		// The copy passes the same policy as a project, with the copies
		// dir as its allowed root.
		paths, err := pathpolicy.Validate(dest, nil, pathpolicy.Default(home, dirs.Copies()))
		if err != nil {
			return "", err
		}
		if rel, err := filepath.Rel(project, paths.Project); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return "", fmt.Errorf("the copy %s would be inside the project", paths.Project)
		}
		return paths.Project, nil
	}()
	if err != nil {
		os.RemoveAll(dest)
		return "", err
	}
	fmt.Fprintf(os.Stderr, "no-history: the session runs on a copy that keeps only the last commit, its message, author and date included, at %s\n", copied)
	return copied, nil
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
// session has one, up to where the agent or shell runs, in lifecycle's
// order. stop tears both down; on an error, nothing is left running.
func startSession(sess *session, lease *sessionregistry.Lease, model string, auto, skipCheck bool) (*docker.Session, func(), error) {
	sess.printWarnings(os.Stderr)
	if n := len(sess.mounts.Masked); n > 0 {
		fmt.Fprintf(os.Stderr, "masked %d secret path(s); run 'kota check' to list them\n", n)
	}
	var catalog models.Catalog
	st := sess.startup(os.Stderr)
	st.Configure = func(ds *docker.Session) error {
		printEmptyDeps(os.Stderr, sess, ds.EmptyDeps())
		var err error
		catalog, err = sess.configure(ds, model, auto)
		return err
	}
	st.Verify = func(ds *docker.Session) error {
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
			fmt.Fprintln(os.Stderr, "approval:", sess.agent.Approval(auto))
			fmt.Fprintln(os.Stderr, "agent override:", overrideSource(sess.agent.Overrides()))
		}
		if skipCheck {
			fmt.Fprintln(os.Stderr, "WARNING: --unsafe-skip-check: containment probes skipped; the sandbox is NOT verified.")
			return nil
		}
		results, err := check.Run(ds, sess.gateway, sess.mounts)
		if err != nil {
			return fmt.Errorf("containment probes: %w", err)
		}
		if failed := probe.Failed(results); len(failed) > 0 {
			printResults(os.Stderr, failed)
			return fmt.Errorf("refusing to start: %d of %d containment probes failed (run 'kota check' for details)", len(failed), len(results))
		}
		fmt.Fprintf(os.Stderr, "containment verified: %d probes passed\n", len(results))
		if sess.engine == nil {
			return nil
		}
		results, err = sess.engine.Probes(ds, sess.engine.ProbeTargets(sess.gateway))
		if err != nil {
			return fmt.Errorf("Engine VM probes: %w", err)
		}
		if failed := probe.Failed(results); len(failed) > 0 {
			printResults(os.Stderr, failed)
			return fmt.Errorf("refusing to start: %d of %d Engine VM probes failed (run 'kota check' for details)", len(failed), len(results))
		}
		fmt.Fprintf(os.Stderr, "Engine VM containment verified: %d probes passed\n", len(results))
		return nil
	}
	return st.Start()
}

// startup is how sess starts: its Engine VM, if any, and its sandbox. The
// caller adds the Configure and Verify steps.
func (s *session) startup(out io.Writer) lifecycle.Startup[*docker.Session] {
	st := lifecycle.Startup[*docker.Session]{
		EngineService: sandbox.EngineService,
		StartSandbox:  func() (*docker.Session, error) { return docker.Start(s.spec, s.sessionDir, s.gatewayKey) },
		Out:           out,
	}
	// Only a non-nil Engine VM, so the interface isn't a typed nil.
	if s.engine != nil {
		st.Engine = s.engine
	}
	return st
}

func printModelClashes(w io.Writer, model string, clashes []sessionregistry.Session) {
	for _, other := range clashes {
		fmt.Fprintf(w, "WARNING: model %s differs from %s in %s (%s, PID %d); llama-swap will swap models back and forth.\n",
			model, other.Model, other.Project, other.Agent, other.PID)
	}
}

// reportChanges warns about watched files, and the files in watch, that the
// session added, modified or deleted, says what changed in them or how to
// see it, and returns them.
func reportChanges(w io.Writer, project string, watch []string, before changereport.Files) []changereport.Change {
	after, err := changereport.SnapshotWith(project, watch)
	if err != nil {
		fmt.Fprintf(w, "WARNING: could not check for changed files that run things: %v\n", err)
		return nil
	}
	changes := changereport.Diff(before, after)
	if len(changes) == 0 {
		return nil
	}
	fmt.Fprintf(w, "WARNING: the session changed %d file(s) that can run code on the host. Review them before building, installing or opening the project in an editor:\n", len(changes))
	for _, line := range changereport.Report(project, before, after, changes) {
		fmt.Fprintln(w, "  "+line)
	}
	return changes
}

// commitCheck is a project's git refs, recorded before a command runs
// project code in a container, to report the commits made while it ran.
type commitCheck struct {
	project string
	// where says which repository the report is about, after "git
	// history changed": "" for the project, " in the copy" for a
	// No-history copy.
	where  string
	before changereport.Refs
	// after, history and err are set by finish.
	after   changereport.Refs
	history changereport.History
	err     error
}

// startCommitCheck records the refs of project. It returns nil for a
// project git can't work in from inside the container, and after a warning
// when the refs can't be read: the command still runs.
func startCommitCheck(w io.Writer, project string) *commitCheck {
	refs, ok, err := changereport.ReadRefs(project, offlineRunner)
	if err != nil {
		fmt.Fprintf(w, "WARNING: could not record git refs, so commits made while it runs won't be checked: %v\n", err)
		return nil
	}
	if !ok {
		return nil
	}
	return &commitCheck{project: project, before: refs}
}

// finish reads the refs and the commits made since c was started. It runs
// once the containers are gone but before the session's cleanup: git needs
// the seeded files in .git, and Docker Desktop's file sharing can still
// show a removed file as present.
func (c *commitCheck) finish() {
	if c == nil {
		return
	}
	c.after, _, c.err = changereport.ReadRefs(c.project, offlineRunner)
	if c.err == nil {
		c.history, c.err = changereport.ReadHistory(c.project, offlineRunner, c.before, c.after)
	}
}

// report lists the branches, tags and commits that finish found, and warns
// about paths in those commits that need review. reported are the watched
// files the working tree report already listed.
func (c *commitCheck) report(w io.Writer, layout mask.Layout, reported []changereport.Change) {
	if c == nil {
		return
	}
	if c.err != nil {
		fmt.Fprintf(w, "WARNING: could not check for new commits: %v\n", c.err)
		return
	}
	h := c.history
	lines := changereport.HistoryLines(c.before, c.after, h)
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(w, "git history changed%s:\n", c.where)
	for _, line := range lines {
		fmt.Fprintln(w, "  "+line)
	}
	paths := commitPaths(layout, reported)
	ignored, err := globalExcludes()
	if err != nil {
		fmt.Fprintf(w, "WARNING: could not read your global git excludes, so commits aren't checked against them: %v\n", err)
	}
	paths.Ignored = ignored
	warnings := changereport.CheckCommits(h.Commits, paths)
	if len(warnings) == 0 {
		return
	}
	fmt.Fprintf(w, "WARNING: %d path(s) in these commits need review before you push or merge them:\n", len(warnings))
	for _, line := range changereport.WarningLines(warnings) {
		fmt.Fprintln(w, "  "+line)
	}
}

// commitPaths are the paths the commit check looks for: the layout's
// placeholders and masks outside .git, and its extra watched files.
func commitPaths(layout mask.Layout, reported []changereport.Change) changereport.CommitPaths {
	p := changereport.CommitPaths{Watch: layout.Watch, Reported: reported}
	for _, m := range layout.Mounts {
		if mask.InGitDir(m.Path) {
			continue
		}
		rel := m.Path
		if m.Dir {
			rel += "/"
		}
		switch {
		case m.Placeholder:
			p.Placeholders = append(p.Placeholders, rel)
		case m.Mask:
			p.Masked = append(p.Masked, rel)
		}
	}
	return p
}

// maxGlobalExcludes caps the size of the global excludes file kota reads.
const maxGlobalExcludes = 1 << 20

// globalExcludes returns whether the host's global git excludes file
// ignores a path, or nil when there is no such file. Only the user's global
// config is read, from outside any repository: a repository config the
// agent wrote could name any file. The file is matched on the host and
// never reaches a container.
func globalExcludes() (func(rel string) bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("git", "config", "--global", "--type=path", "--get", "core.excludesFile")
	cmd.Dir = "/"
	out, err := cmd.Output()
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		return nil, fmt.Errorf("git config --global core.excludesFile: %w", err)
	}
	file := strings.TrimSpace(string(out))
	if file == "" {
		file = filepath.Join(instructions.ConfigHome(os.Getenv, home), "git", "ignore")
	}
	info, err := os.Stat(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, err
	case !info.Mode().IsRegular():
		return nil, fmt.Errorf("%s is not a regular file", file)
	case info.Size() > maxGlobalExcludes:
		return nil, fmt.Errorf("%s is larger than %d bytes", file, maxGlobalExcludes)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	ig, err := mask.ParseIgnore(data, file)
	if err != nil {
		return nil, err
	}
	return ig.Ignored, nil
}

func cmdCheck(args []string) error {
	sa, err := parseSessionArgs("check", args, nil)
	if err != nil {
		return err
	}
	sa.command = nil
	sa.engine, sa.engineOptional = true, true
	lease, err := lockProject(sa, "check")
	if err != nil {
		return err
	}
	defer releaseProject(lease)
	depPlan, err := sessionDeps(lease.paths.Project, lease.paths.Project)
	if err != nil {
		return err
	}
	sess, err := prepareSession(sa, sandbox.ModeRun, lease.paths, lease.paths.Project, depPlan)
	if err != nil {
		return err
	}
	defer sess.cleanup()

	printMounts(os.Stdout, sess.spec)
	printMasks(os.Stdout, sess.layout)
	fmt.Fprintf(os.Stdout, "instructions: %s ('kota instructions' prints them)\n", sess.style.Source)
	fmt.Fprintln(os.Stdout, "agent override:", overrideSource(sess.agent.Overrides()))
	sess.printWarnings(os.Stdout)
	engineErr := printEngine(os.Stdout, sess)

	// check reports what it finds instead of stopping at the first problem:
	// a gateway problem is also reported by the gateway probe, and the
	// sandbox probes run when the Engine VM can't start.
	var modelErr error
	var failed, engineFailed []probe.Result
	st := sess.startup(os.Stdout)
	if sess.engine != nil {
		st.Engine = &reportedEngine{Engine: sess.engine, failed: &engineErr}
	}
	st.Configure = func(ds *docker.Session) error {
		printEmptyDeps(os.Stdout, sess, ds.EmptyDeps())
		var catalog models.Catalog
		catalog, modelErr = sess.configure(ds, sa.model, false)
		printModels(os.Stdout, catalog, modelErr)
		return nil
	}
	st.Verify = func(ds *docker.Session) error {
		start := time.Now()
		results, err := check.Run(ds, sess.gateway, sess.mounts)
		if err != nil {
			return fmt.Errorf("containment probes: %w", err)
		}
		fmt.Println("\nprobes:")
		printResults(os.Stdout, results)
		failed = probe.Failed(results)
		fmt.Printf("\n%d passed, %d failed (%s)\n", len(results)-len(failed), len(failed), time.Since(start).Round(10*time.Millisecond))
		if sess.engine == nil || engineErr != nil {
			return nil
		}
		start = time.Now()
		engineResults, err := sess.engine.Probes(ds, sess.engine.ProbeTargets(sess.gateway))
		if err != nil {
			return fmt.Errorf("Engine VM probes: %w", err)
		}
		fmt.Println("\nEngine VM probes:")
		printResults(os.Stdout, engineResults)
		engineFailed = probe.Failed(engineResults)
		fmt.Printf("\n%d passed, %d failed (%s)\n", len(engineResults)-len(engineFailed), len(engineFailed), time.Since(start).Round(10*time.Millisecond))
		return nil
	}
	_, stop, err := st.Start()
	if err != nil {
		return err
	}
	stop()
	switch {
	case len(failed) > 0:
		return errors.New("sandbox is not contained")
	case len(engineFailed) > 0:
		return errors.New("the Engine VM is not contained")
	case engineErr != nil:
		return engineErr
	}
	return modelErr
}

// reportedEngine is check's Engine VM: one that doesn't get ready is
// reported, and recorded in failed, instead of stopping the check.
type reportedEngine struct {
	*enginesession.Engine
	failed *error
}

func (e *reportedEngine) Ready(w io.Writer) error {
	if err := e.Engine.Ready(w); err != nil {
		fmt.Fprintf(w, "engine status: can't start: %v\n\n", err)
		*e.failed = errors.New("the Engine VM can't start")
	}
	return nil
}

// printEngine shows the project's Engine VM: none, or its declared images
// with the digests prep recorded, and why it can't start if it can't. It
// returns that reason.
func printEngine(w io.Writer, sess *session) error {
	cfg, cfgErr := engine.Read(sess.project)
	if cfg == nil && sess.engineErr == nil {
		if cfgErr != nil {
			fmt.Fprintf(w, "engine: none (%v)\n\n", cfgErr)
		} else {
			fmt.Fprintf(w, "engine: none (no engine section in %s)\n\n", engine.ConfigFile)
		}
		return nil
	}
	fmt.Fprintln(w, "engine:")
	if sess.engine != nil {
		for _, a := range sess.engine.Archives() {
			fmt.Fprintf(w, "  image %s (%s)\n", a.Ref, a.Digest)
		}
	} else if cfg != nil {
		for _, ref := range cfg.Images {
			fmt.Fprintf(w, "  image %s\n", ref)
		}
	}
	if sess.engineErr != nil {
		fmt.Fprintf(w, "  status: can't start: %v\n\n", sess.engineErr)
		return fmt.Errorf("the Engine VM can't start: %w", sess.engineErr)
	}
	fmt.Fprintln(w)
	return nil
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
	cache, err := engineImages(project)
	if err != nil {
		return err
	}
	var declared []string
	if engineCfg != nil {
		declared = engineCfg.Images
	}
	images, err := cache.Plan(declared)
	if err != nil {
		return err
	}
	if err := confirmPrep(project, depPlan, images); err != nil {
		return err
	}
	sess, err := prepareSession(sa, sandbox.ModePrep, lease.paths, lease.paths.Project, depPlan)
	if err != nil {
		return err
	}
	before, err := changereport.SnapshotWith(sess.project, sess.layout.Watch)
	if err != nil {
		sess.cleanup()
		return fmt.Errorf("snapshot watched files: %w", err)
	}
	commits := startCommitCheck(os.Stderr, sess.project)
	err = runPrep(sess, cache, images)
	commits.finish()
	sess.cleanup()
	reported := reportChanges(os.Stderr, sess.project, sess.layout.Watch, before)
	commits.report(os.Stderr, sess.layout, reported)
	if err != nil {
		return err
	}
	// The next prep asks about changes made after this point.
	record, err := prepgate.Snapshot(sess.project)
	record.Dirs = sess.deps.Dirs
	var file string
	if err == nil {
		file, err = prepRecordFile(sess.project)
	}
	if err == nil {
		err = record.Save(file)
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
	dirs, err := hostdata.Default()
	if err != nil {
		return err
	}
	data, err := dirs.ProjectData(project)
	if err != nil {
		return err
	}
	if len(volumes) == 0 && data.Empty() {
		fmt.Fprintf(out, "nothing to clean for %s\n", project)
		return nil
	}
	fmt.Fprintf(out, "Clean %s.\n", project)
	if len(volumes) > 0 {
		fmt.Fprintln(out, "Volumes to remove:")
		for _, name := range volumes {
			fmt.Fprintln(out, "  "+name)
		}
	}
	if data.EngineDir != "" {
		fmt.Fprintf(out, "Engine VM data to remove: %s\n", data.EngineDir)
		for _, ref := range data.Images {
			fmt.Fprintln(out, "  image archive "+ref)
		}
	}
	if len(data.Sessions) > 0 {
		fmt.Fprintln(out, "Leftover session directories to remove (from sessions that didn't end cleanly; they may hold Engine VM TLS certificates):")
		for _, dir := range data.Sessions {
			fmt.Fprintln(out, "  "+dir)
		}
	}
	if len(data.Copies) > 0 {
		fmt.Fprintln(out, "Kept --no-history copies to remove (commits or changes made in them that weren't brought back are lost):")
		for _, dir := range data.Copies {
			fmt.Fprintln(out, "  "+dir)
		}
	}
	if shared {
		fmt.Fprintln(out, "Shared caches are included; other projects will need to fetch dependencies again.")
	}
	if data.PrepRecord != "" {
		fmt.Fprintln(out, "The project's prep confirmation record will also be reset.")
	}
	fmt.Fprint(out, "Type y to clean: ")
	if !interrupt.Ask(func() bool { return readYes(in) }) {
		return errors.New("clean not run")
	}
	if err := docker.RemoveVolumes(volumes...); err != nil {
		return err
	}
	for _, path := range data.Paths() {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}
	fmt.Fprintln(out, "project cleaned; run 'kota prep' before the next session if dependencies or Engine images are needed")
	return nil
}

// prepRecordFile is where the project's state after its last prep is kept,
// on the host and out of the sandbox's reach.
func prepRecordFile(project string) (string, error) {
	dirs, err := hostdata.Default()
	if err != nil {
		return "", err
	}
	return dirs.PrepRecord(project), nil
}

// confirmPrep asks the user to type y before a prep whose inputs or Engine
// images changed since the last prep, or before the first one: an agent
// session may have written what prep is about to run with network access.
// images is nil for a project without Engine images.
func confirmPrep(project string, plan deps.Plan, images *engine.ImagePlan) error {
	d, err := prepDecision(project, plan)
	if err != nil {
		return err
	}
	if !d.Ask() && (images == nil || !images.Changed()) {
		return nil
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
	file, err := prepRecordFile(project)
	if err != nil {
		return prepgate.Decision{}, err
	}
	last, err := prepgate.Load(file)
	if err != nil {
		return prepgate.Decision{}, fmt.Errorf("read the last prep's record: %w", err)
	}
	now, err := prepgate.Snapshot(project)
	if err != nil {
		return prepgate.Decision{}, err
	}
	return prepgate.Decide(last, now, len(plan.Hooks) > 0), nil
}

func printPrepGate(w io.Writer, project string, plan deps.Plan, images *engine.ImagePlan, d prepgate.Decision) {
	switch {
	case d.First:
		fmt.Fprintf(w, "First prep of %s. These run with network access:\n", project)
		for _, s := range plan.Steps {
			for _, c := range []string{s.Fetch, s.Script} {
				if c != "" {
					fmt.Fprintf(w, "  %s: %s\n", s.Name, c)
				}
			}
		}
	case d.Ask():
		fmt.Fprintf(w, "%s changed since its last prep, and prep runs with network access.\n", project)
		if len(d.Inputs) > 0 {
			fmt.Fprintln(w, "Files that decide what prep runs:")
			for _, line := range changereport.Warnings(d.Inputs) {
				fmt.Fprintln(w, "  "+line)
			}
		}
	default:
		fmt.Fprintf(w, "The engine images of %s changed since its last prep.\n", project)
	}
	if images != nil && images.Changed() {
		fmt.Fprintln(w, "Engine images, downloaded with crane (no project code runs):")
		for _, ref := range images.Added {
			fmt.Fprintf(w, "  new      %s\n", ref)
		}
		for _, m := range images.Moved {
			fmt.Fprintf(w, "  moved    %s: %s -> %s\n", m.Ref, shortDigest(m.From), shortDigest(m.To))
		}
		for _, ref := range images.Removed {
			fmt.Fprintf(w, "  removed  %s\n", ref)
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
	if !d.First && d.Ask() {
		fmt.Fprintf(w, "Review them first, e.g. git -C %s diff\n", project)
	}
}

// shortDigest is a digest's algorithm and first 12 hex digits, as docker
// shows image IDs.
func shortDigest(d string) string {
	if alg, hex, ok := strings.Cut(d, ":"); ok && len(hex) > 12 {
		return alg + ":" + hex[:12]
	}
	return d
}

// readYes reads one line and reports whether it is y or yes.
func readYes(r io.Reader) bool {
	line, _ := bufio.NewReader(r).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// runPrep runs the install steps with network access, without an agent,
// the gateway or its key, and carries out the Engine images' plan if set.
// The containers are gone when it returns.
func runPrep(sess *session, cache engineimages.Cache, images *engine.ImagePlan) error {
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
	if images != nil {
		if err := cache.Fetch(images); err != nil {
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
	fmt.Fprintln(os.Stderr, "dependencies installed; 'kota run' uses them offline")
	return nil
}

// engineImages is project's Engine image cache. crane runs in a throwaway
// container of the base image.
func engineImages(project string) (engineimages.Cache, error) {
	layout, err := engineLayout()
	if err != nil {
		return engineimages.Cache{}, err
	}
	c := engineimages.Cache{
		Dir: layout.ArchiveDir(project),
		Run: func(work, script string) error {
			return docker.RunOnline(baseImage, os.Getuid(), os.Getgid(), work, "sh", "-c", script)
		},
		Out: os.Stderr,
	}
	// A host the Engine VM doesn't support can still prep a project
	// without images; Fetch refuses downloads.
	if m, err := engine.MachineFor(engineHost()); err == nil {
		c.Platform = m.Platform
	}
	return c, nil
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

// printEmptyDeps hints at prep for each dependency volume that is empty.
func printEmptyDeps(w io.Writer, sess *session, empty []string) {
	for _, d := range sess.deps.Dirs {
		if slices.Contains(empty, sandbox.Workspace+"/"+d.Path) {
			fmt.Fprintf(w, "hint: %s is empty although %s exists; run 'kota prep %s' to install dependencies\n", d.Path, d.Manifest, sess.key)
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
	if err := s.prepared.Configure(catalog, auto, keyEnv); err != nil {
		return models.Catalog{}, err
	}
	return catalog, nil
}

// confirmFullAuto asks the user to type y before a full-auto session on a
// project with uncommitted changes. It returns an error when they decline.
// fullAuto is whether the agent acts without asking in this session, and
// always whether it would without --auto too.
func confirmFullAuto(project, agent string, fullAuto, always bool) error {
	if !fullAuto {
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
		if !always {
			msg += ", or run without --auto"
		}
		return errors.New(msg)
	}
	return nil
}

// offlineRunner runs git for approval.ReadStatus and the commit check in
// the base image, offline
// and read-only, so filters or an fsmonitor the agent configured never run
// on the host. Paths outside the project, such as a linked worktree's git
// dir, must pass the --ro path policy.
func offlineRunner(mounts []string, dir string, cmd ...string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	policy := pathpolicy.Default(home, os.Getenv("KOTA_ALLOWED_ROOT"))
	for _, m := range mounts {
		if err := policy.CheckReadOnly(m); err != nil {
			return "", err
		}
	}
	return docker.RunOffline(baseImage, os.Getuid(), os.Getgid(), mounts, dir, cmd...)
}

// writableRunner is offlineRunner with one writable mount: the copies dir
// for the clone, the bundle's directory for the export, the original's git
// dir for the import. Every mount, the writable one too, must pass the
// --ro path policy.
func writableRunner(mounts []string, writable, dir string, cmd ...string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	policy := pathpolicy.Default(home, os.Getenv("KOTA_ALLOWED_ROOT"))
	for _, m := range append(slices.Clip(mounts), writable) {
		if err := policy.CheckReadOnly(m); err != nil {
			return "", err
		}
	}
	return docker.RunOfflineWritable(baseImage, os.Getuid(), os.Getgid(), mounts, writable, dir, cmd...)
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

// printResults prints one row per probe. The name column is as wide as the
// longest name, so the columns after it line up.
func printResults(w io.Writer, results []probe.Result) {
	width := 0
	for _, r := range results {
		width = max(width, len(r.Name))
	}
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
		fmt.Fprintf(w, "  %s  %-*s %-13s %s\n", status, width, r.Name, want, outcome)
	}
}

// newSessionDir creates a per-session directory in kota's data
// directory, which the Docker VM shares.
func newSessionDir(project string) (string, error) {
	dirs, err := hostdata.Default()
	if err != nil {
		return "", err
	}
	return dirs.NewSessionDir(project)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

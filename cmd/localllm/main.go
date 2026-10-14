// Command localllm runs coding agents in an offline Docker sandbox that can
// reach only the local model gateway.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"localllm/assets"
	"localllm/internal/check"
	"localllm/internal/docker"
	"localllm/internal/opencode"
	"localllm/internal/pathpolicy"
	"localllm/internal/probe"
	"localllm/internal/sandbox"
)

const (
	baseImage      = "localllm-base:latest"
	forwarderImage = "localllm-forwarder:latest"
	defaultGateway = "http://host.docker.internal:8080"
	defaultModel   = "qwen3.6-coder"

	agentConfigPath = "/etc/localllm/opencode.json"
)

const usage = `usage: localllm <command> [flags]

commands:
  build                 build the sandbox images
  run [flags] [dir] [-- cmd...]
                        verify containment, then start opencode offline in
                        dir (default: current dir)
  check [flags] [dir]   show mounts and run the containment probes

flags:
  --gateway URL         model gateway (default $LOCALLLM_GATEWAY or ` + defaultGateway + `)
  --ro PATH             mount PATH read-only at ` + sandbox.ReadOnlyRoot + `/<name> (repeatable)
  --unsafe-skip-check   run only: start without the containment probes

dir must be inside the allowed root ($LOCALLLM_ALLOWED_ROOT, default ~/Projects).
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "build":
		err = cmdBuild()
	case "run":
		err = cmdRun(os.Args[2:])
	case "check":
		err = cmdCheck(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "localllm:", err)
		os.Exit(1)
	}
}

func cmdBuild() error {
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
	return nil
}

// session holds everything needed to start one sandbox.
type session struct {
	spec       sandbox.Compose
	gateway    sandbox.Gateway
	sessionDir string
	// readOnly are the in-sandbox targets of the --ro mounts.
	readOnly []string
}

// sessionArgs are the [flags] [dir] [-- cmd...] shared by run and check.
type sessionArgs struct {
	gatewayURL string
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
	gw := fset.String("gateway", envOr("LOCALLLM_GATEWAY", defaultGateway), "model gateway URL")
	var ro stringList
	fset.Var(&ro, "ro", "mount `PATH` read-only (repeatable)")
	if register != nil {
		register(fset)
	}
	if err := fset.Parse(args); err != nil {
		return sessionArgs{}, err
	}

	sa := sessionArgs{gatewayURL: *gw, dir: ".", ro: ro}
	rest := fset.Args()
	for i, a := range rest {
		if a == "--" {
			sa.command = rest[i+1:]
			rest = rest[:i]
			break
		}
	}
	switch len(rest) {
	case 0:
	case 1:
		sa.dir = rest[0]
	default:
		return sessionArgs{}, fmt.Errorf("%s takes at most one directory", name)
	}
	return sa, nil
}

// prepareSession validates paths, renders the agent config and builds the
// compose spec. The caller removes sessionDir when done.
func prepareSession(sa sessionArgs) (*session, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	paths, err := pathpolicy.Validate(sa.dir, sa.ro, pathpolicy.Default(home, os.Getenv("LOCALLLM_ALLOWED_ROOT")))
	if err != nil {
		return nil, err
	}
	project := paths.Project
	gw, err := sandbox.ParseGateway(sa.gatewayURL)
	if err != nil {
		return nil, err
	}

	sessionDir, err := newSessionDir(project)
	if err != nil {
		return nil, err
	}
	cfg, err := opencode.Render(opencode.Settings{GatewayURL: sandbox.AgentGatewayURL(), Model: defaultModel})
	if err != nil {
		os.RemoveAll(sessionDir)
		return nil, err
	}
	cfgFile := filepath.Join(sessionDir, "opencode.json")
	if err := os.WriteFile(cfgFile, cfg, 0o644); err != nil {
		os.RemoveAll(sessionDir)
		return nil, err
	}

	spec := sandbox.Build(sandbox.Inputs{
		ProjectDir:     project,
		UID:            os.Getuid(),
		GID:            os.Getgid(),
		Gateway:        gw,
		AgentImage:     baseImage,
		ForwarderImage: forwarderImage,
		ReadOnly:       append([]sandbox.Mount{{Source: cfgFile, Target: agentConfigPath}}, paths.ReadOnly...),
		Env:            opencode.Env(agentConfigPath),
		Command:        sa.command,
	})
	var readOnly []string
	for _, m := range paths.ReadOnly {
		readOnly = append(readOnly, m.Target)
	}
	return &session{spec: spec, gateway: gw, sessionDir: sessionDir, readOnly: readOnly}, nil
}

func cmdRun(args []string) error {
	var skipCheck bool
	sa, err := parseSessionArgs("run", args, func(f *flag.FlagSet) {
		f.BoolVar(&skipCheck, "unsafe-skip-check", false, "start without running the containment probes")
	})
	if err != nil {
		return err
	}
	sess, err := prepareSession(sa)
	if err != nil {
		return err
	}
	defer os.RemoveAll(sess.sessionDir)

	ds, err := docker.Start(sess.spec, sess.sessionDir)
	if err != nil {
		return err
	}
	defer ds.Close()

	if skipCheck {
		fmt.Fprintln(os.Stderr, "WARNING: --unsafe-skip-check: containment probes skipped; the sandbox is NOT verified.")
	} else {
		results, err := check.Run(ds, sess.gateway, sess.readOnly)
		if err != nil {
			return fmt.Errorf("containment probes: %w", err)
		}
		if failed := probe.Failed(results); len(failed) > 0 {
			printResults(os.Stderr, failed)
			return fmt.Errorf("refusing to start: %d of %d containment probes failed (run 'localllm check' for details)", len(failed), len(results))
		}
		fmt.Fprintf(os.Stderr, "containment verified: %d probes passed\n", len(results))
	}
	return ds.Interactive()
}

func cmdCheck(args []string) error {
	sa, err := parseSessionArgs("check", args, nil)
	if err != nil {
		return err
	}
	sa.command = nil
	sess, err := prepareSession(sa)
	if err != nil {
		return err
	}
	defer os.RemoveAll(sess.sessionDir)

	printMounts(os.Stdout, sess.spec)

	ds, err := docker.Start(sess.spec, sess.sessionDir)
	if err != nil {
		return err
	}
	defer ds.Close()

	start := time.Now()
	results, err := check.Run(ds, sess.gateway, sess.readOnly)
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
	return nil
}

func printMounts(w io.Writer, spec sandbox.Compose) {
	fmt.Fprintln(w, "mounts:")
	for _, v := range spec.Services[sandbox.AgentService].Volumes {
		mode := "rw"
		if v.ReadOnly {
			mode = "ro"
		}
		fmt.Fprintf(w, "  %s  %s -> %s\n", mode, v.Source, v.Target)
	}
	fmt.Fprintf(w, "gateway (in sandbox): %s\n", sandbox.AgentGatewayURL())
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

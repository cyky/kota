// Command localllm runs coding agents in an offline Docker sandbox that can
// reach only the local model gateway.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"localllm/assets"
	"localllm/internal/docker"
	"localllm/internal/opencode"
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
                        start opencode offline in dir (default: current dir)

run flags:
  --gateway URL         model gateway (default $LOCALLLM_GATEWAY or ` + defaultGateway + `)
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

func cmdRun(args []string) error {
	fset := flag.NewFlagSet("run", flag.ContinueOnError)
	gatewayURL := fset.String("gateway", envOr("LOCALLLM_GATEWAY", defaultGateway), "model gateway URL")
	if err := fset.Parse(args); err != nil {
		return err
	}

	dir, command := ".", []string(nil)
	rest := fset.Args()
	for i, a := range rest {
		if a == "--" {
			command = rest[i+1:]
			rest = rest[:i]
			break
		}
	}
	switch len(rest) {
	case 0:
	case 1:
		dir = rest[0]
	default:
		return errors.New("run takes at most one directory")
	}

	project, err := resolveDir(dir)
	if err != nil {
		return err
	}
	gw, err := sandbox.ParseGateway(*gatewayURL)
	if err != nil {
		return err
	}

	sessionDir, err := newSessionDir(project)
	if err != nil {
		return err
	}
	defer os.RemoveAll(sessionDir)

	cfg, err := opencode.Render(opencode.Settings{GatewayURL: sandbox.AgentGatewayURL(), Model: defaultModel})
	if err != nil {
		return err
	}
	cfgFile := filepath.Join(sessionDir, "opencode.json")
	if err := os.WriteFile(cfgFile, cfg, 0o644); err != nil {
		return err
	}

	spec := sandbox.Build(sandbox.Inputs{
		ProjectDir:     project,
		UID:            os.Getuid(),
		GID:            os.Getgid(),
		Gateway:        gw,
		AgentImage:     baseImage,
		ForwarderImage: forwarderImage,
		ReadOnly:       []sandbox.Mount{{Source: cfgFile, Target: agentConfigPath}},
		Env:            opencode.Env(agentConfigPath),
		Command:        command,
	})
	return docker.RunSession(spec, sessionDir)
}

func resolveDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	return resolved, nil
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

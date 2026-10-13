// Package docker is a thin adapter over the docker and docker compose CLIs.
package docker

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"localllm/internal/sandbox"
)

// BuildImage builds an image from a build context held in an fs.FS.
func BuildImage(tag string, context fs.FS) error {
	dir, err := os.MkdirTemp("", "localllm-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := os.CopyFS(dir, context); err != nil {
		return fmt.Errorf("copy build context: %w", err)
	}
	return run("docker", "build", "-t", tag, dir)
}

// RunSession writes spec into sessionDir and runs the agent service
// interactively, tearing the forwarder and networks down afterwards.
func RunSession(spec sandbox.Compose, sessionDir string) error {
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	file := filepath.Join(sessionDir, "compose.json")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return err
	}
	compose := []string{"compose", "-f", file, "-p", spec.Name}
	defer run("docker", append(compose, "down", "--remove-orphans", "--timeout", "1")...)
	return run("docker", append(compose, "run", "--rm", sandbox.AgentService)...)
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

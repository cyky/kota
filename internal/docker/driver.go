// Package docker is a thin adapter over the docker and docker compose CLIs.
package docker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

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

// Session is a started sandbox: networks and forwarder are up, the agent
// container is run on demand. Close tears everything down.
type Session struct {
	spec    sandbox.Compose
	compose []string
	// env is the environment of every docker compose command: the host's
	// plus the session's secrets, which services take by name (PassEnv).
	env []string
	// gatewayKey reports whether a gateway key is in env.
	gatewayKey bool
}

// Start writes spec into sessionDir and brings up the forwarder and networks.
// gatewayKey, when not empty, is passed to compose in its environment as
// sandbox.GatewayKeyEnv, never in the compose file or on a command line.
func Start(spec sandbox.Compose, sessionDir, gatewayKey string) (*Session, error) {
	data, err := json.MarshalIndent(spec.Literal(), "", "  ")
	if err != nil {
		return nil, err
	}
	file := filepath.Join(sessionDir, "compose.json")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return nil, err
	}
	s := &Session{spec: spec, compose: []string{"compose", "-f", file, "-p", spec.Name}, env: os.Environ()}
	if gatewayKey != "" {
		s.env = append(s.env, sandbox.GatewayKeyEnv+"="+gatewayKey)
		s.gatewayKey = true
	}
	if _, err := s.output("up", "-d", "--quiet-pull", sandbox.ForwarderService); err != nil {
		s.Close()
		return nil, fmt.Errorf("start forwarder: %w", err)
	}
	return s, nil
}

// NetworkGateways returns the gateway address of each non-internal network
// of the session, i.e. the Docker VM as seen from that network. Internal
// networks run in isolated mode and have no gateway address.
func (s *Session) NetworkGateways() ([]string, error) {
	var names []string
	for name, n := range s.spec.Networks {
		if !n.Internal {
			names = append(names, s.spec.Name+"_"+name)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	args := append([]string{"network", "inspect", "--format", "{{range .IPAM.Config}}{{.Gateway}} {{end}}"}, names...)
	out, err := exec.Command("docker", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("inspect networks: %w", err)
	}
	var addrs []string
	for _, f := range strings.Fields(string(out)) {
		if net.ParseIP(f) != nil {
			addrs = append(addrs, f)
		}
	}
	return addrs, nil
}

// VMInterfaces returns the IPv4 interface prefixes of the Docker VM itself,
// read from a throwaway container on the host network.
func (s *Session) VMInterfaces() ([]netip.Prefix, error) {
	image := s.spec.Services[sandbox.ForwarderService].Image
	out, err := exec.Command("docker", "run", "--rm", "--network", "host", "--entrypoint", "ip", image, "-4", "-o", "addr").Output()
	if err != nil {
		return nil, fmt.Errorf("list VM interfaces: %w", err)
	}
	var prefixes []netip.Prefix
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "inet" {
				if p, err := netip.ParsePrefix(f[i+1]); err == nil {
					prefixes = append(prefixes, p)
				}
			}
		}
	}
	return prefixes, nil
}

// ResolveFromForwarder resolves host the way a container with normal
// networking would, which is how host.docker.internal gets its address.
func (s *Session) ResolveFromForwarder(host string) ([]string, error) {
	out, err := s.output("exec", "-T", sandbox.ForwarderService, "getent", "hosts", host)
	if err != nil {
		return nil, err
	}
	var addrs []string
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			addrs = append(addrs, f[0])
		}
	}
	return addrs, nil
}

// ErrUnauthorized is returned by GatewayGet when the gateway rejects the
// request for a missing or wrong API key.
var ErrUnauthorized = errors.New("the gateway requires an API key, or rejected the one configured")

// GatewayGet fetches path from the model gateway through the forwarder,
// the same route the agent's requests take. The key is expanded inside the
// forwarder, so it isn't on the host's command line.
func (s *Session) GatewayGet(path string) ([]byte, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d%s", sandbox.GatewayPort, path)
	args := []string{"exec", "-T"}
	script := `exec wget -q -T 10 -O - "$1"`
	if s.gatewayKey {
		args = append(args, "-e", sandbox.GatewayKeyEnv)
		script = `exec wget -q -T 10 --header "Authorization: Bearer $` + sandbox.GatewayKeyEnv + `" -O - "$1"`
	}
	args = append(args, sandbox.ForwarderService, "sh", "-c", script, "sh", url)
	out, err := s.output(args...)
	if err != nil {
		if strings.Contains(err.Error(), " 401 ") {
			err = ErrUnauthorized
		}
		return nil, fmt.Errorf("fetch %s from the gateway: %w", path, err)
	}
	return []byte(out), nil
}

// Capture runs cmd in a fresh agent container without a TTY and returns its
// stdout. A non-zero exit is not an error; the output is what matters.
func (s *Session) Capture(cmd ...string) (string, error) {
	args := append(append(slices.Clone(s.compose), "run", "--rm", "-T", sandbox.AgentService), cmd...)
	c := exec.Command("docker", args...)
	c.Env = s.env
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			return "", err
		}
		if stdout.Len() == 0 {
			return "", fmt.Errorf("agent container failed: %s", strings.TrimSpace(stderr.String()))
		}
	}
	return stdout.String(), nil
}

// Interactive runs the agent service attached to the terminal.
func (s *Session) Interactive() error {
	cmd := exec.Command("docker", append(slices.Clone(s.compose), "run", "--rm", sandbox.AgentService)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = s.env
	return cmd.Run()
}

// Close removes the session's containers and networks.
func (s *Session) Close() {
	s.output("down", "--remove-orphans", "--timeout", "1")
}

func (s *Session) output(args ...string) (string, error) {
	c := exec.Command("docker", append(slices.Clone(s.compose), args...)...)
	c.Env = s.env
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

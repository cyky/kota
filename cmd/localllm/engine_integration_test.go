//go:build integration

// Engine VM integration tests boot real Engine VMs, so they need QEMU and
// the Engine base disk ('localllm build') as well as Docker. Prep needs
// network access. Run with: go test -tags integration ./cmd/localllm
package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"localllm/assets"
	"localllm/internal/check"
	"localllm/internal/docker"
	"localllm/internal/engine"
	"localllm/internal/enginevm"
	"localllm/internal/models"
	"localllm/internal/probe"
	"localllm/internal/sandbox"
)

func requireEngine(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker not available")
	}
	m, err := engine.MachineFor(engineHost())
	if err != nil {
		t.Skip(err)
	}
	if _, err := enginevm.FindQEMU(m); err != nil {
		t.Skip(err)
	}
	layout, err := engineLayout()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := layout.LoadBase(engineHost().Arch); err != nil {
		t.Skip(err)
	}
	if err := exec.Command("docker", "run", "--rm", baseImage, "sh", "-c", "command -v crane && command -v docker").Run(); err != nil {
		t.Skip("the base image lacks crane or the Docker CLI; run 'localllm build'")
	}
}

// engineProject creates a Go project with a testcontainers Postgres test
// under the user cache dir, which Docker Desktop-style VMs share, makes its
// parent the allowed root, and removes its engine data afterwards.
func engineProject(t *testing.T) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(cache, "localllm-test-")
	if err != nil {
		t.Fatal(err)
	}
	root, _ = filepath.EvalSymlinks(root)
	t.Cleanup(func() { os.RemoveAll(root) })
	t.Setenv("LOCALLLM_ALLOWED_ROOT", root)
	project := filepath.Join(root, "app")
	files := map[string]string{
		engine.ConfigFile: "engine:\n  images: [postgres:16-alpine]\n",
		"go.mod":          "module example.com/app\n\ngo 1.27\n",
		"db_test.go": `package app

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestPostgres(t *testing.T) {
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("app"), postgres.WithUsername("app"), postgres.WithPassword("app"),
		postgres.BasicWaitStrategies())
	if err != nil {
		t.Fatal(err)
	}
	defer ctr.Terminate(ctx)
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(ctx, "SELECT 40 + 2").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 42 {
		t.Fatalf("got %d", n)
	}
	t.Logf("POSTGRES-QUERY-OK via %s", dsn)
}
`,
	}
	for rel, content := range files {
		p := filepath.Join(project, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"go", "get", "github.com/testcontainers/testcontainers-go/modules/postgres@v0.44.0", "github.com/jackc/pgx/v5@latest"},
		{"go", "mod", "tidy"},
		{"git", "init", "-q"},
	} {
		c := exec.Command(args[0], args[1:]...)
		c.Dir = project
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	layout, err := engineLayout()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(layout.ProjectDir(project)) })
	return project
}

// renameCaches gives the shared cache volumes test-only names, so the test
// neither uses nor fills the real caches, and removes them afterwards.
func renameCaches(t *testing.T) func(*sandbox.Compose) {
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

// stubModelGateway serves the default model on host loopback, so sessions
// can configure the agent, and returns its URL as the sandbox reaches it.
func stubModelGateway(t *testing.T) string {
	t.Helper()
	f, err := models.ParseFile(assets.Models)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":[{"id":%q}]}`, f.Default)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return "http://host.docker.internal:" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

// prepEngineProject runs prep as the CLI does after its confirmation:
// dependencies and engine images, with test-only shared caches.
func prepEngineProject(t *testing.T, project string, caches func(*sandbox.Compose)) {
	t.Helper()
	sa := sessionArgs{dir: project}
	lease, err := lockProject(sa, "prep")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseProject(lease)
	depPlan, err := detectDeps(project)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := engine.Read(project)
	if err != nil || cfg == nil {
		t.Fatalf("engine config = %+v, %v", cfg, err)
	}
	sess, err := prepareSession(sa, sandbox.ModePrep, lease.paths, depPlan)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.cleanup()
	caches(&sess.spec)
	caches(&sess.fetchSpec)
	if err := runPrep(sess, cfg); err != nil {
		t.Fatal(err)
	}
}

// withShellSession starts the project's shell session up to where Bash
// would run, with the Engine VM, the containment probes and test-only
// caches, and calls fn with it. The session and VM are gone when it
// returns.
func withShellSession(t *testing.T, project, gateway string, caches func(*sandbox.Compose), fn func(*session, *docker.Session)) {
	t.Helper()
	sa := sessionArgs{agent: defaultAgent, gatewayURL: gateway, dir: project, shell: true, engine: true}
	lease, err := lockProject(sa, "shell")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseProject(lease)
	depPlan, err := sessionDeps(project)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := prepareSession(sa, sandbox.ModeRun, lease.paths, depPlan)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.cleanup()
	if sess.engine == nil {
		t.Fatal("no Engine VM in the shell session")
	}
	caches(&sess.spec)
	ds, stop, err := startSession(sess, lease.Lease, "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	vm := sess.engine.running
	func() {
		defer stop()
		fn(sess, ds)
	}()
	if qemuRunning(vm) {
		t.Error("the Engine VM still runs after the session")
	}
}

// shellSession runs script in a fresh agent container of a shell session
// (withShellSession) and returns the output.
func shellSession(t *testing.T, project, gateway string, caches func(*sandbox.Compose), script string) string {
	t.Helper()
	var out string
	withShellSession(t, project, gateway, caches, func(_ *session, ds *docker.Session) {
		var err error
		if out, err = ds.Capture("bash", "-c", "exec 2>&1\n"+script); err != nil {
			t.Fatal(err)
		}
	})
	return out
}

// qemuRunning reports whether vm's QEMU process still exists.
func qemuRunning(vm *enginevm.VM) bool {
	pid := vm.PID()
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

func TestEngineInShell(t *testing.T) {
	requireEngine(t)
	t.Setenv("LOCALLLM_GATEWAY_KEY", "")
	t.Setenv("LOCALLLM_GATEWAY_KEY_FILE", "")
	project := engineProject(t)
	caches := renameCaches(t)
	gateway := stubModelGateway(t)
	prepEngineProject(t, project, caches)

	layout, err := engineLayout()
	if err != nil {
		t.Fatal(err)
	}
	rec, err := engine.LoadRecord(layout.ArchiveDir(project))
	if err != nil || rec == nil || len(rec.Archives) != 1 || !strings.HasPrefix(rec.Archives[0].Digest, "sha256:") {
		t.Fatalf("archive record = %+v, %v", rec, err)
	}

	t.Run("testcontainers postgres offline", func(t *testing.T) {
		out := shellSession(t, project, gateway, caches, `
env | grep -E '^(DOCKER_|TESTCONTAINERS_)' | sort
docker images --format '{{.Repository}}:{{.Tag}}'
go test -count=1 -v ./... && echo GO-TEST-PASSED`)
		for _, want := range []string{"DOCKER_HOST=tcp://engine:2376", "TESTCONTAINERS_RYUK_DISABLED=true", "postgres:16-alpine", "POSTGRES-QUERY-OK", "GO-TEST-PASSED"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("only the session's client certificate", func(t *testing.T) {
		other, err := engine.NewCerts(time.Now(), []string{"engine"})
		if err != nil {
			t.Fatal(err)
		}
		if err := other.WriteClient(filepath.Join(project, "other-certs")); err != nil {
			t.Fatal(err)
		}
		out := shellSession(t, project, gateway, caches, `
c=$DOCKER_CERT_PATH
curl -fsS --max-time 5 --cacert $c/ca.pem --cert $c/cert.pem --key $c/key.pem https://engine:2376/_ping && echo; echo "own=$?"
curl -fsS --max-time 5 --cacert $c/ca.pem https://engine:2376/_ping; echo "none=$?"
o=/workspace/other-certs
curl -fsS --max-time 5 --cacert $c/ca.pem --cert $o/cert.pem --key $o/key.pem https://engine:2376/_ping; echo "other=$?"
curl -fsS --max-time 5 http://engine:2376/_ping; echo "plain=$?"
DOCKER_CERT_PATH=$o docker version >/dev/null 2>&1; echo "cli-other=$?"`)
		if !strings.Contains(out, "OK\nown=0") {
			t.Errorf("own certificate refused:\n%s", out)
		}
		for _, refused := range []string{"none=0", "other=0", "plain=0", "cli-other=0"} {
			if strings.Contains(out, refused) {
				t.Errorf("%s: the engine accepted a client without the session's certificate:\n%s", strings.TrimSuffix(refused, "=0"), out)
			}
		}
	})

	t.Run("clean disk every session", func(t *testing.T) {
		out := shellSession(t, project, gateway, caches, `
docker run --rm -v /:/host postgres:16-alpine sh -c 'echo planted > /host/etc/localllm-planted' && echo PLANTED
docker tag postgres:16-alpine planted:1 && echo TAGGED`)
		if !strings.Contains(out, "PLANTED") || !strings.Contains(out, "TAGGED") {
			t.Fatalf("couldn't plant:\n%s", out)
		}
		out = shellSession(t, project, gateway, caches, `
docker run --rm -v /:/host postgres:16-alpine sh -c 'test -e /host/etc/localllm-planted' && echo FILE-SURVIVED
docker image inspect planted:1 >/dev/null 2>&1 && echo TAG-SURVIVED
docker image inspect postgres:16-alpine >/dev/null 2>&1 && echo IMAGE-LOADED`)
		if strings.Contains(out, "SURVIVED") || !strings.Contains(out, "IMAGE-LOADED") {
			t.Errorf("state carried over between sessions:\n%s", out)
		}
	})

	t.Run("route closes when the agent powers the VM off", func(t *testing.T) {
		out := shellSession(t, project, gateway, caches, `
docker version --format 'API UP {{.Server.Version}}'
docker run --rm --privileged postgres:16-alpine sh -c 'echo o > /proc/sysrq-trigger'
for i in $(seq 1 50); do getent hosts engine >/dev/null || break; sleep 0.2; done
getent hosts engine >/dev/null && echo ROUTE-OPEN || echo ROUTE-CLOSED`)
		if !strings.Contains(out, "API UP") || !strings.Contains(out, "ROUTE-CLOSED") {
			t.Errorf("engine route after the VM powered off:\n%s", out)
		}
	})

	t.Run("engine probes pass and detect an open route", func(t *testing.T) {
		withShellSession(t, project, gateway, caches, func(sess *session, ds *docker.Session) {
			// startSession ran the probes and removed their image.
			if err := sess.engine.running.LoadTar(sess.engine.probeTar, engineProbeImage); err != nil {
				t.Fatal(err)
			}
			results, err := sess.engine.probes(ds, sess.gateway)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range results {
				if !r.Passed() {
					t.Errorf("engine probe %q failed: exit %d (missing %v)", r.Name, r.ExitCode, r.Missing)
				}
			}
			if len(results) < 15 {
				t.Errorf("only %d engine probes", len(results))
			}
			// The VM's own address is open to itself, so a must-fail probe
			// aimed at it has to report the route.
			if err := sess.engine.running.LoadTar(sess.engine.probeTar, engineProbeImage); err != nil {
				t.Fatal(err)
			}
			results, err = check.RunEngine(ds, probe.EngineTargets{
				Image: engineProbeImage, GuestAPIPort: engine.GuestAPIPort,
				HostAddrs: []string{"10.0.2.15"}, HostPorts: []int{engine.GuestAPIPort},
				EngineHost: sandbox.EngineHost, APIPort: engine.GuestAPIPort, CertDir: sandbox.EngineCertDir,
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range results {
				if r.Name == "engine host 10.0.2.15:2376" && (r.Passed() || r.Missing) {
					t.Errorf("open route not detected: %+v", r)
				}
			}
		})
	})

	t.Run("check shows the engine", func(t *testing.T) {
		t.Setenv("LOCALLLM_GATEWAY", gateway)
		cmd, output := startLocalllm(t, "check", project)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("check failed: %v\n%s", err, output)
			}
		case <-time.After(3 * time.Minute):
			t.Fatalf("check didn't finish:\n%s", output)
		}
		for _, want := range []string{"image postgres:16-alpine (sha256:", "Engine VM ready", "Engine VM probes:", "engine internet 1.1.1.1:443", "engine published port"} {
			if !strings.Contains(output.String(), want) {
				t.Errorf("check output lacks %q:\n%s", want, output)
			}
		}
		if strings.Contains(output.String(), "FAIL ") {
			t.Errorf("a probe failed:\n%s", output)
		}
	})

	t.Run("stops on interrupt", func(t *testing.T) {
		pidfile := layout.Pidfile(project)
		t.Setenv("LOCALLLM_GATEWAY", gateway)
		cmd, output := startLocalllm(t, "shell", project)
		waitFor(t, "the Engine VM to start", func() bool {
			_, err := os.Stat(pidfile)
			return err == nil
		}, output)
		data, _ := os.ReadFile(pidfile)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if code := interruptAndWait(t, cmd, output); code != 130 {
			t.Errorf("exit code = %d, want 130; output:\n%s", code, output)
		}
		if pid > 0 && syscall.Kill(pid, 0) == nil {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("QEMU (PID %d) still runs after the interrupt", pid)
		}
		if _, err := os.Stat(pidfile); !os.IsNotExist(err) {
			t.Errorf("pidfile left: %v", err)
		}
	})
}

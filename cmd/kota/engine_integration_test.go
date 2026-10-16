//go:build integration

// Engine VM integration tests boot real Engine VMs, so they need QEMU and
// the Engine base disk ('kota build') as well as Docker. Prep needs
// network access. Run with: go test -tags integration ./cmd/kota
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

	"kota/assets"
	"kota/internal/docker"
	"kota/internal/engine"
	"kota/internal/enginevm"
	"kota/internal/models"
	"kota/internal/opencode"
	"kota/internal/pi"
	"kota/internal/sandbox"
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
		t.Skip("the base image lacks crane or the Docker CLI; run 'kota build'")
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
	root, err := os.MkdirTemp(cache, "kota-test-")
	if err != nil {
		t.Fatal(err)
	}
	root, _ = filepath.EvalSymlinks(root)
	t.Cleanup(func() { os.RemoveAll(root) })
	t.Setenv("KOTA_ALLOWED_ROOT", root)
	project := filepath.Join(root, "app")
	files := map[string]string{
		// Not the default size, so the test sees the configured one applied.
		engine.ConfigFile: "engine:\n  images: [postgres:16-alpine]\n  cpus: 3\n  memory: 3G\n",
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
		"undeclared_test.go": `package app

import (
	"context"
	"testing"

	"github.com/testcontainers/testcontainers-go"
)

func TestUndeclared(t *testing.T) {
	_, err := testcontainers.Run(context.Background(), "redis:7.4-alpine")
	t.Logf("UNDECLARED-ERR: %v", err)
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
// dependencies and engine images, with test-only shared caches. It returns
// the images' plan.
func prepEngineProject(t *testing.T, project string, caches func(*sandbox.Compose)) *engine.ImagePlan {
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
	cache, err := engineImages(project)
	if err != nil {
		t.Fatal(err)
	}
	images, err := cache.Plan(cfg.Images)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := prepareSession(sa, sandbox.ModePrep, lease.paths, lease.paths.Project, depPlan)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.cleanup()
	caches(&sess.spec)
	caches(&sess.fetchSpec)
	if err := runPrep(sess, cache, images); err != nil {
		t.Fatal(err)
	}
	return images
}

// withShellSession starts the project's shell session up to where Bash
// would run, with the Engine VM, the containment probes and test-only
// caches, and calls fn with it. The session and VM are gone when it
// returns.
func withShellSession(t *testing.T, project, gateway string, caches func(*sandbox.Compose), fn func(*session, *docker.Session)) {
	t.Helper()
	withSession(t, sessionArgs{agent: defaultAgent, gatewayURL: gateway, dir: project, shell: true, engine: true}, caches, fn)
}

// withSession is withShellSession for any session args, e.g. a run of an
// agent, which starts as cmdOffline starts it. Without sa.engine, no
// Engine VM may start.
func withSession(t *testing.T, sa sessionArgs, caches func(*sandbox.Compose), fn func(*session, *docker.Session)) {
	t.Helper()
	operation := "run"
	if sa.shell {
		operation = "shell"
	}
	lease, err := lockProject(sa, operation)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseProject(lease)
	// Only --no-history goes through offlinePaths: it would also ask the
	// full-auto question, which these sessions never answer.
	paths, key := lease.paths, lease.paths.Project
	if sa.noHistory {
		if paths, key, err = offlinePaths(sa, lease, sa.agent, false); err != nil {
			t.Fatal(err)
		}
	}
	depPlan, err := sessionDeps(paths.Project, key)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := prepareSession(sa, sandbox.ModeRun, paths, key, depPlan)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.cleanup()
	if (sess.engine != nil) != sa.engine {
		t.Fatalf("Engine VM in the session: %v, want %v", sess.engine != nil, sa.engine)
	}
	caches(&sess.spec)
	ds, stop, err := startSession(sess, lease.Lease, "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer stop()
		fn(sess, ds)
	}()
	if sess.engine != nil && qemuRunning(sess.engine.PID()) {
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

// qemuRunning reports whether the QEMU process pid still exists.
func qemuRunning(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

func TestEngineInShell(t *testing.T) {
	requireEngine(t)
	t.Setenv("KOTA_GATEWAY_KEY", "")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
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

	t.Run("unchanged images aren't downloaded again", func(t *testing.T) {
		archive := filepath.Join(layout.ArchiveDir(project), rec.Archives[0].File)
		before, err := os.Stat(archive)
		if err != nil {
			t.Fatal(err)
		}
		p := prepEngineProject(t, project, caches)
		if p.Changed() || len(p.Downloads) != 0 || len(p.Keep) != 1 {
			t.Errorf("second prep's plan = %+v", p)
		}
		after, err := os.Stat(archive)
		if err != nil || !os.SameFile(before, after) {
			t.Errorf("archive replaced or gone: %v", err)
		}
		again, err := engine.LoadRecord(layout.ArchiveDir(project))
		if err != nil || again == nil || len(again.Archives) != 1 || again.Archives[0] != rec.Archives[0] {
			t.Errorf("record after the second prep = %+v, %v", again, err)
		}
	})

	t.Run("testcontainers postgres offline", func(t *testing.T) {
		out := shellSession(t, project, gateway, caches, `
env | grep -E '^(DOCKER_|TESTCONTAINERS_)' | sort
docker images --format '{{.Repository}}:{{.Tag}}'
go test -count=1 -v -run TestPostgres ./... && echo GO-TEST-PASSED`)
		for _, want := range []string{"DOCKER_HOST=tcp://engine:2376", "TESTCONTAINERS_RYUK_DISABLED=true", "postgres:16-alpine", "POSTGRES-QUERY-OK", "GO-TEST-PASSED"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("an undeclared image fails without a download, and the note explains it", func(t *testing.T) {
		start := time.Now()
		out := shellSession(t, project, gateway, caches, `
docker run --rm redis:7.4-alpine true; echo "cli=$?"
go test -count=1 -v -run TestUndeclared ./... 2>&1 | grep UNDECLARED-ERR
cat `+opencodeInstructionsPath)
		t.Logf("took %s:\n%s", time.Since(start), out)
		for _, want := range []string{
			"Unable to find image 'redis:7.4-alpine' locally", "cli=125",
			// testcontainers' error names only the registry it couldn't
			// reach, so the note tells the agent to find the image in the test.
			"UNDECLARED-ERR: generic container: create container: Error response from daemon: Get \"https://registry-1.docker.io/v2/\"",
			"which you can't change: postgres:16-alpine.", "run 'kota prep' on the host",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q", want)
			}
		}
		if strings.Contains(out, "UNDECLARED-ERR: <nil>") {
			t.Error("the undeclared image started")
		}
	})

	t.Run("the configured size", func(t *testing.T) {
		out := shellSession(t, project, gateway, caches, `docker info --format 'SIZE {{.NCPU}} {{.MemTotal}}'`)
		var cpus, mem int64
		if i := strings.Index(out, "SIZE "); i < 0 {
			t.Fatalf("no size:\n%s", out)
		} else if _, err := fmt.Sscanf(out[i:], "SIZE %d %d", &cpus, &mem); err != nil {
			t.Fatalf("%v:\n%s", err, out)
		}
		// The kernel keeps some of the memory for itself.
		if cpus != 3 || mem <= 2700<<20 || mem > 3<<30 {
			t.Errorf("Engine VM has %d CPUs and %d MiB, want 3 and about 3072", cpus, mem>>20)
		}
	})

	notes := map[string]string{opencode.Agent: opencodeInstructionsPath, pi.Agent: piAgentDir + "/" + pi.AppendSystem}
	for _, agent := range []string{opencode.Agent, pi.Agent} {
		t.Run("run starts the Engine VM for "+agent, func(t *testing.T) {
			sa, err := parseOfflineArgs("run", []string{"--agent", agent, "--gateway", gateway, project}, nil)
			if err != nil {
				t.Fatal(err)
			}
			withSession(t, sa, caches, func(_ *session, ds *docker.Session) {
				out, err := ds.Capture("bash", "-c", `exec 2>&1
echo "engine=$KOTA_ENGINE"
docker image inspect postgres:16-alpine >/dev/null && echo IMAGE-LOADED
cat `+notes[agent]+`
echo x > `+notes[agent]+` || echo NOTE-READ-ONLY
touch "$(dirname `+notes[agent]+`)/state" && echo DIR-WRITABLE`)
				if err != nil {
					t.Fatal(err)
				}
				for _, want := range []string{"engine=available", "IMAGE-LOADED", "KOTA_ENGINE=available", "DOCKER_HOST", "NOTE-READ-ONLY", "DIR-WRITABLE"} {
					if !strings.Contains(out, want) {
						t.Errorf("output lacks %q:\n%s", want, out)
					}
				}
			})
		})
	}

	// shell --no-engine takes the same path; TestNoEngine covers it.
	t.Run("run --no-engine", func(t *testing.T) {
		sa, err := parseOfflineArgs("run", []string{"--no-engine", "--gateway", gateway, project}, nil)
		if err != nil {
			t.Fatal(err)
		}
		withSession(t, sa, caches, func(_ *session, ds *docker.Session) {
			if _, err := os.Stat(layout.Pidfile(project)); !os.IsNotExist(err) {
				t.Errorf("Engine VM pidfile with --no-engine: %v", err)
			}
			out, err := ds.Capture("bash", "-c", `exec 2>&1
echo "engine=$KOTA_ENGINE docker_host=$DOCKER_HOST"
getent hosts engine || echo NO-ENGINE-ROUTE
cat `+opencodeInstructionsPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"engine=unavailable docker_host=\n", "NO-ENGINE-ROUTE", "not run"} {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
		})
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
docker run --rm -v /:/host postgres:16-alpine sh -c 'echo planted > /host/etc/kota-planted' && echo PLANTED
docker tag postgres:16-alpine planted:1 && echo TAGGED`)
		if !strings.Contains(out, "PLANTED") || !strings.Contains(out, "TAGGED") {
			t.Fatalf("couldn't plant:\n%s", out)
		}
		out = shellSession(t, project, gateway, caches, `
docker run --rm -v /:/host postgres:16-alpine sh -c 'test -e /host/etc/kota-planted' && echo FILE-SURVIVED
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
			// startSession ran the probes once; each run loads the probe
			// image again and removes it afterwards.
			results, err := sess.engine.Probes(ds, sess.engine.ProbeTargets(sess.gateway))
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
			targets := sess.engine.ProbeTargets(sess.gateway)
			targets.HostAddrs, targets.HostPorts = []string{"10.0.2.15"}, []int{engine.GuestAPIPort}
			results, err = sess.engine.Probes(ds, targets)
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
		t.Setenv("KOTA_GATEWAY", gateway)
		cmd, output := startKota(t, "check", project)
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
		t.Setenv("KOTA_GATEWAY", gateway)
		cmd, output := startKota(t, "shell", project)
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

	// Last: it changes kota.yaml.
	t.Run("a failed download names the image and keeps the archives", func(t *testing.T) {
		const bad = "localhost:1/missing:1"
		yaml := "engine:\n  images: [postgres:16-alpine, " + bad + "]\n"
		if err := os.WriteFile(filepath.Join(project, engine.ConfigFile), []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := engine.Read(project)
		if err != nil {
			t.Fatal(err)
		}
		cache, err := engineImages(project)
		if err != nil {
			t.Fatal(err)
		}
		p, err := cache.Plan(cfg.Images)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Added) != 1 || p.Added[0] != bad || len(p.Downloads) != 1 || len(p.Keep) != 1 {
			t.Fatalf("plan = %+v", p)
		}
		err = cache.Fetch(p)
		if err == nil || !strings.Contains(err.Error(), "download engine image "+bad) {
			t.Fatalf("err = %v, want it to name %s", err, bad)
		}
		after, err := engine.LoadRecord(layout.ArchiveDir(project))
		if err != nil || after == nil || len(after.Archives) != 1 || after.Archives[0] != rec.Archives[0] {
			t.Errorf("record after a failed download = %+v, %v", after, err)
		}
		if info, err := os.Lstat(filepath.Join(layout.ArchiveDir(project), rec.Archives[0].File)); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			t.Errorf("previous archive: %v", err)
		}
	})
}

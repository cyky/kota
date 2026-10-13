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
	"strconv"
	"strings"
	"testing"

	"localllm/assets"
	"localllm/internal/docker"
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
	gw := stubGateway(t)
	spec := sandbox.Build(sandbox.Inputs{
		ProjectDir:     projectDir(t),
		UID:            os.Getuid(),
		GID:            os.Getgid(),
		Gateway:        gw,
		AgentImage:     testAgentImage,
		ForwarderImage: testForwarderImage,
	})
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

	results, err := Run(s, gw)
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

	results, err := Run(s, gw)
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

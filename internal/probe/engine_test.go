package probe

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func engineTargets() EngineTargets {
	return EngineTargets{
		Image:        "kota-engine-probe:latest",
		GuestAPIPort: 2376,
		HostAddrs:    []string{"10.0.2.2", "192.168.1.1", "10.0.2.2"},
		HostPorts:    []int{8080, 22, 51234},
		EngineHost:   "engine",
		APIPort:      2376,
		CertDir:      "/etc/kota/engine",
	}
}

func TestEngineSuiteCoversEscapeRoutes(t *testing.T) {
	probes := byName(EngineSuite(engineTargets()))
	inVMMustFail := []string{
		"engine internet 1.1.1.1:443",
		"engine internet 8.8.8.8:53",
		"engine internet [2606:4700:4700::1111]:443",
		"engine cloud metadata 169.254.169.254:80",
		"engine dns example.com",
		"engine dns example.com via 10.0.2.3",
		"engine dns example.com via 1.1.1.1",
		"engine host 10.0.2.2:8080",
		"engine host 10.0.2.2:22",
		"engine host 10.0.2.2:51234",
		"engine host 192.168.1.1:8080",
	}
	for _, name := range inVMMustFail {
		p, ok := probes[name]
		if !ok {
			t.Errorf("missing probe %q", name)
			continue
		}
		if p.MustSucceed || !p.InEngine {
			t.Errorf("probe %q: must fail inside the VM (%+v)", name, p)
		}
	}
	for name, inVM := range map[string]bool{
		"engine vm itself 127.0.0.1:2376":           true,
		"engine API with the session's certificate": false,
		"engine published port":                     false,
	} {
		if p, ok := probes[name]; !ok || !p.MustSucceed || p.InEngine != inVM {
			t.Errorf("probe %q missing or not must-succeed %v: %+v", name, inVM, p)
		}
	}
	if p := probes["engine API without a certificate"]; p.MustSucceed || p.InEngine || len(p.Cmd) == 0 {
		t.Errorf("API without certificate = %+v, want a must-fail sandbox probe", p)
	}
	if strings.Contains(strings.Join(probes["engine API without a certificate"].Cmd, " "), "--cert ") {
		t.Error("the no-certificate probe sends a certificate")
	}
	n := 0
	for name := range probes {
		if strings.HasPrefix(name, "engine host 10.0.2.2:") {
			n++
		}
	}
	if n != 3 {
		t.Errorf("%d probes for 10.0.2.2, want 3 (deduplicated, one per port)", n)
	}
}

// TestEngineScript runs the script with stub docker, socat, nslookup and
// curl. docker runs the in-VM part locally, and the published port is a
// real listener, so every probe produces a result.
func TestEngineScript(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte(listenerGreeting + "\n"))
			c.Close()
		}
	}()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)

	bin := t.TempDir()
	log := filepath.Join(bin, "docker.log")
	stubs := map[string]string{
		// docker run --rm ... --entrypoint sh IMAGE -c SCRIPT runs SCRIPT.
		"docker": `printf '%s\n' "$*" >> ` + log + `
case "$1 $2" in
"run --rm") while [ "$1" != -c ]; do shift; done; exec sh "$@";;
"port "*) echo "0.0.0.0:` + port + `";;
esac`,
		"socat":    `case "$3" in *127.0.0.1*) exit 0;; *) exit 1;; esac`,
		"nslookup": `exit 1`,
		"curl":     `for a; do [ "$a" = --cert ] && exit 0; done; exit 35`,
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tg := engineTargets()
	tg.EngineHost = "127.0.0.1"
	probes := EngineSuite(tg)
	cmd := exec.Command("bash", "-c", EngineScript(probes, tg.Image))
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range Parse(probes, string(out)) {
		if !r.Passed() {
			t.Errorf("probe %q: exit %d, missing %v", r.Name, r.ExitCode, r.Missing)
		}
	}
	// QEMU's forwards drop connections in parallel bursts, so the sandbox
	// probes, which share the API's forward, run one at a time.
	for _, line := range strings.Split(EngineScript(probes, tg.Image), "\n") {
		if strings.Contains(line, "/_ping") && strings.HasSuffix(line, "&") {
			t.Errorf("API probe runs in parallel: %s", line)
		}
	}
	calls, _ := os.ReadFile(log)
	for _, want := range []string{"run -d --pull never --name kota-engine-probe -p 8080 --entrypoint socat", "--network host", "rm -f kota-engine-probe", "rmi kota-engine-probe:latest"} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("docker calls lack %q:\n%s", want, calls)
		}
	}
}

// TestEngineScriptMissingPortFails checks the published-port probe fails
// when the listener couldn't be published.
func TestEngineScriptMissingPortFails(t *testing.T) {
	bin := t.TempDir()
	for name, body := range map[string]string{"docker": "exit 1", "socat": "exit 0", "nslookup": "exit 1", "curl": "exit 0"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	probes := EngineSuite(engineTargets())
	cmd := exec.Command("bash", "-c", EngineScript(probes, "img"))
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, _ := cmd.Output()
	results := byResult(Parse(probes, string(out)))
	if r := results["engine published port"]; r.Passed() {
		t.Errorf("published port passed without a listener: %+v", r)
	}
	// docker failed, so the in-VM probes have no results and fail.
	if r := results["engine internet 1.1.1.1:443"]; !r.Missing || r.Passed() {
		t.Errorf("in-VM probe without the probe container = %+v", r)
	}
}

func byResult(results []Result) map[string]Result {
	m := map[string]Result{}
	for _, r := range results {
		m[r.Name] = r
	}
	return m
}

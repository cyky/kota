package probe

import (
	"os/exec"
	"strings"
	"testing"
)

func testTargets() Targets {
	return Targets{
		GatewayURL:  "http://gateway:8080",
		GatewayHost: "gateway",
		GatewayPort: 8080,
		HostAddrs:   []string{"192.168.5.2", "192.168.1.1", "192.168.5.2"},
		HostPorts:   []int{8080, 22},
		Workspace:   "/workspace",
	}
}

func byName(probes []Probe) map[string]Probe {
	m := map[string]Probe{}
	for _, p := range probes {
		m[p.Name] = p
	}
	return m
}

func TestSuiteCoversEscapeRoutes(t *testing.T) {
	probes := byName(Suite(testTargets()))

	mustFail := []string{
		"internet 1.1.1.1:443",
		"internet dns example.com",
		"dns host.docker.internal",
		"gateway other port gateway:22",
		"host 192.168.5.2:8080",
		"host 192.168.5.2:22",
		"host 192.168.1.1:8080",
		"host 192.168.1.1:22",
	}
	for _, name := range mustFail {
		p, ok := probes[name]
		if !ok {
			t.Errorf("missing probe %q", name)
			continue
		}
		if p.MustSucceed {
			t.Errorf("probe %q must fail", name)
		}
	}
	for _, name := range []string{"gateway /v1/models", "workspace writable"} {
		if p, ok := probes[name]; !ok || !p.MustSucceed {
			t.Errorf("probe %q missing or not must-succeed", name)
		}
	}
}

func TestSuiteDeduplicatesHostAddrs(t *testing.T) {
	n := 0
	for _, p := range Suite(testTargets()) {
		if strings.HasPrefix(p.Name, "host 192.168.5.2:") {
			n++
		}
	}
	if n != 2 {
		t.Errorf("got %d probes for 192.168.5.2, want 2 (one per port)", n)
	}
}

func TestResultPassed(t *testing.T) {
	tests := []struct {
		name string
		r    Result
		want bool
	}{
		{"must fail, failed", Result{Probe: Probe{}, ExitCode: 1}, true},
		{"must fail, timed out", Result{Probe: Probe{}, ExitCode: 124}, true},
		{"must fail, succeeded", Result{Probe: Probe{}, ExitCode: 0}, false},
		{"must succeed, succeeded", Result{Probe: Probe{MustSucceed: true}, ExitCode: 0}, true},
		{"must succeed, failed", Result{Probe: Probe{MustSucceed: true}, ExitCode: 7}, false},
		{"missing must fail", Result{Probe: Probe{}, Missing: true}, false},
		{"missing must succeed", Result{Probe: Probe{MustSucceed: true}, Missing: true}, false},
	}
	for _, tt := range tests {
		if got := tt.r.Passed(); got != tt.want {
			t.Errorf("%s: Passed() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestParse(t *testing.T) {
	probes := []Probe{{Name: "a"}, {Name: "b", MustSucceed: true}, {Name: "c"}}
	out := "noise\r\nLOCALLLM-PROBE 1 0\r\nLOCALLLM-PROBE 0 7\nLOCALLLM-PROBE 9 0\nLOCALLLM-PROBE x 0\n"

	results := Parse(probes, out)
	if results[0].Missing || results[0].ExitCode != 7 || !results[0].Passed() {
		t.Errorf("a = %+v", results[0])
	}
	if results[1].Missing || !results[1].Passed() {
		t.Errorf("b = %+v", results[1])
	}
	if !results[2].Missing || results[2].Passed() {
		t.Errorf("c = %+v, want missing and failed", results[2])
	}
	if f := Failed(results); len(f) != 1 || f[0].Name != "c" {
		t.Errorf("Failed = %+v", f)
	}
}

// TestScriptRuns executes a rendered script with local bash to check quoting
// and result reporting end to end.
func TestScriptRuns(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("timeout"); err != nil {
		t.Skip("timeout not available")
	}
	probes := []Probe{
		{Name: "true", Cmd: []string{"true"}, MustSucceed: true},
		{Name: "false", Cmd: []string{"false"}},
		{Name: "quoting", Cmd: []string{"sh", "-c", `test "$1" = "it's a 'test'"`, "sh", "it's a 'test'"}, MustSucceed: true},
	}
	out, err := exec.Command("bash", "-c", Script(probes)).Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range Parse(probes, string(out)) {
		if !r.Passed() {
			t.Errorf("%s: %+v\noutput:\n%s", r.Name, r, out)
		}
	}
}

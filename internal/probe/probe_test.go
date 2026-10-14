package probe

import (
	"os"
	"os/exec"
	"path/filepath"
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

// TestReadOnlyProbe runs the read-only probe locally: it may only pass when
// the path exists and rejects writes.
func TestReadOnlyProbe(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	dir := t.TempDir()
	writable := filepath.Join(dir, "writable")
	locked := filepath.Join(dir, "locked")
	lockedFile := filepath.Join(dir, "locked.md")
	for _, d := range []string{writable, locked} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(lockedFile, []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	tt := testTargets()
	tt.ReadOnly = []string{writable, locked, lockedFile, filepath.Join(dir, "missing")}
	want := map[string]bool{writable: false, locked: true, lockedFile: true, filepath.Join(dir, "missing"): false}

	for _, p := range Suite(tt) {
		path, ok := strings.CutPrefix(p.Name, "read-only ")
		if !ok {
			continue
		}
		if p.MustSucceed {
			t.Errorf("probe %q must fail", p.Name)
		}
		err := exec.Command(p.Cmd[0], p.Cmd[1:]...).Run()
		passed := Result{Probe: p, ExitCode: exitCode(err)}.Passed()
		if passed != want[path] {
			t.Errorf("probe %q passed = %v, want %v", p.Name, passed, want[path])
		}
		delete(want, path)
	}
	if len(want) > 0 {
		t.Errorf("no probes for %v", want)
	}
	if entries, _ := os.ReadDir(writable); len(entries) > 0 {
		t.Errorf("probe left files behind: %v", entries)
	}
}

func exitCode(err error) int {
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}

func TestSuitePinnedProbes(t *testing.T) {
	tt := testTargets()
	tt.Pinned = []string{"/workspace/.git"}
	p, ok := byName(Suite(tt))["pinned /workspace/.git"]
	if !ok || !p.MustSucceed {
		t.Fatalf("pinned probe missing or not must-succeed: %+v", p)
	}
}

// TestMaskedProbe runs the masked probe locally: only an empty file or an
// empty directory passes.
func TestMaskedProbe(t *testing.T) {
	dir := t.TempDir()
	emptyFile := filepath.Join(dir, "empty")
	fullFile := filepath.Join(dir, "full")
	emptyDir := filepath.Join(dir, "emptydir")
	fullDir := filepath.Join(dir, "fulldir")
	hiddenDir := filepath.Join(dir, "hiddendir")
	for _, d := range []string{emptyDir, fullDir, hiddenDir} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range map[string]string{emptyFile: "", fullFile: "secret", filepath.Join(fullDir, "x"): "", filepath.Join(hiddenDir, ".x"): ""} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]bool{emptyFile: true, emptyDir: true, fullFile: false, fullDir: false, hiddenDir: false, filepath.Join(dir, "missing"): false}

	tt := testTargets()
	for path := range want {
		tt.Masked = append(tt.Masked, path)
	}
	for _, p := range Suite(tt) {
		path, ok := strings.CutPrefix(p.Name, "masked ")
		if !ok {
			continue
		}
		passed := Result{Probe: p, ExitCode: exitCode(exec.Command(p.Cmd[0], p.Cmd[1:]...).Run())}.Passed()
		if passed != want[path] {
			t.Errorf("probe %q passed = %v, want %v", p.Name, passed, want[path])
		}
		delete(want, path)
	}
	if len(want) > 0 {
		t.Errorf("no probes for %v", want)
	}
}

// TestStateProbe runs the state probe locally: only a writable directory
// passes, and nothing is left behind.
func TestStateProbe(t *testing.T) {
	if _, ok := byName(Suite(testTargets()))["state writable"]; ok {
		t.Error("state probe without a state dir")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{dir: true, locked: false, filepath.Join(dir, "missing"): false} {
		tt := testTargets()
		tt.State = path
		p, ok := byName(Suite(tt))["state writable"]
		if !ok || !p.MustSucceed {
			t.Fatalf("state probe missing or not must-succeed: %+v", p)
		}
		passed := Result{Probe: p, ExitCode: exitCode(exec.Command(p.Cmd[0], p.Cmd[1:]...).Run())}.Passed()
		if passed != want {
			t.Errorf("%s: passed = %v, want %v", path, passed, want)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("probe left files behind: %v", entries)
	}
}

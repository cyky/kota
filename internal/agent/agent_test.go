package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeOverride writes an Agent override named name under configHome.
func writeOverride(t *testing.T, configHome, name, content string) string {
	t.Helper()
	file := filepath.Join(configHome, "kota", name)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestLoadUnknownAgent(t *testing.T) {
	_, err := Load("claude", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), `unknown agent "claude"; available: opencode, pi`) {
		t.Errorf("err = %v", err)
	}
}

func TestLoadWithoutOverrides(t *testing.T) {
	for _, name := range Names() {
		a, err := Load(name, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if a.Name() != name || len(a.Overrides()) != 0 {
			t.Errorf("%s: name = %q, overrides = %v", name, a.Name(), a.Overrides())
		}
	}
}

func TestLoadOverrides(t *testing.T) {
	config := t.TempDir()
	cli := writeOverride(t, config, "opencode/cli.json", `{"theme": {"name": "system"}}`)
	cfg := writeOverride(t, config, "opencode/opencode.json", `{"compaction": {"auto": false}}`)
	a, err := Load("opencode", config)
	if err != nil {
		t.Fatal(err)
	}
	// In the adapter's order, not the order written.
	if !slices.Equal(a.Overrides(), []string{cfg, cli}) {
		t.Errorf("overrides = %v, want %v", a.Overrides(), []string{cfg, cli})
	}
}

func TestLoadRefusedOverride(t *testing.T) {
	config := t.TempDir()
	writeOverride(t, config, "pi/settings.json", `{"extensions": []}`)
	if _, err := Load("pi", config); err == nil || !strings.Contains(err.Error(), "extensions") {
		t.Errorf("err = %v, want extensions refused", err)
	}
}

func TestFullAuto(t *testing.T) {
	cases := []struct {
		agent string
		auto  bool
		want  bool
	}{
		{"opencode", false, false},
		{"opencode", true, true},
		{"pi", false, true},
		{"pi", true, true},
	}
	for _, c := range cases {
		a, err := Load(c.agent, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if got := a.FullAuto(c.auto); got != c.want {
			t.Errorf("%s FullAuto(%v) = %v, want %v", c.agent, c.auto, got, c.want)
		}
	}
}

func TestApproval(t *testing.T) {
	cases := []struct {
		agent string
		auto  bool
		want  string
	}{
		{"opencode", false, "ask (opencode asks before edits and commands; --auto to skip)"},
		{"opencode", true, "auto (opencode edits files and runs commands without asking)"},
		{"pi", false, "auto (pi has no permission system: it edits files and runs commands without asking, and the sandbox is its only boundary)"},
		{"pi", true, "auto (pi has no permission system: it edits files and runs commands without asking, and the sandbox is its only boundary)"},
	}
	for _, c := range cases {
		a, err := Load(c.agent, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if got := a.Approval(c.auto); got != c.want {
			t.Errorf("%s Approval(%v) = %q, want %q", c.agent, c.auto, got, c.want)
		}
	}
}

func TestBridgeCarriesCLIOverrideInline(t *testing.T) {
	config := t.TempDir()
	writeOverride(t, config, "opencode/cli.json", `{"theme": {"name": "system"}}`)
	a, err := Load("opencode", config)
	if err != nil {
		t.Fatal(err)
	}
	b, err := a.Bridge()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.Env[opencodeCLIEnv], `"system"`) || b.Env["OPENCODE_CONFIG"] != opencodeConfigPath {
		t.Errorf("env = %v", b.Env)
	}
	// The adapter's own env is untouched.
	if _, ok := opencode.env[opencodeCLIEnv]; ok {
		t.Error("the inline override leaked into the adapter's env")
	}
}

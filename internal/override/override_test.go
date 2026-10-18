package override

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var policy = Policy{
	Allowed: []string{"keybinds", "theme"},
	Refused: map[string]string{"attention": "takes file paths"},
}

func write(t *testing.T, content string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "tui.json")
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestPath(t *testing.T) {
	if got := Path("/cfg", "opencode/tui.json"); got != "/cfg/kota/opencode/tui.json" {
		t.Errorf("Path = %q", got)
	}
}

func TestLoadMissing(t *testing.T) {
	over, found, err := Load(filepath.Join(t.TempDir(), "tui.json"), policy)
	if over != nil || found || err != nil {
		t.Errorf("missing file: %v, %v, %v; want nothing found", over, found, err)
	}
}

func TestLoad(t *testing.T) {
	file := write(t, `{"$schema": "https://example.com/tui.json", "keybinds": {"session_delete": "ctrl+backspace"}, "theme": "dark"}`)
	over, found, err := Load(file, policy)
	if err != nil || !found {
		t.Fatalf("found %v, err %v", found, err)
	}
	want := map[string]any{"keybinds": map[string]any{"session_delete": "ctrl+backspace"}, "theme": "dark"}
	if !reflect.DeepEqual(over, want) {
		t.Errorf("override = %v, want %v ($schema dropped)", over, want)
	}
}

func TestLoadRefuses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tui.json")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		file string
		want []string
	}{
		{"refused key", write(t, `{"attention": {}}`), []string{`"attention"`, "takes file paths", "allowed: keybinds, theme"}},
		{"unknown key", write(t, `{"theme": "dark", "plugin": []}`), []string{`"plugin"`, "allowed: keybinds, theme"}},
		{"invalid JSON", write(t, `{"theme": `), []string{"JSON"}},
		{"not an object", write(t, `["theme"]`), []string{"JSON object"}},
		{"trailing data", write(t, `{} {}`), []string{"JSON"}},
		{"not a regular file", dir, []string{"not a regular file"}},
		{"too large", write(t, `{"theme": "`+strings.Repeat("x", MaxSize)+`"}`), []string{"larger than"}},
	}
	for _, c := range cases {
		_, _, err := Load(c.file, policy)
		if err == nil {
			t.Errorf("%s: no error", c.name)
			continue
		}
		for _, w := range append(c.want, c.file) {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: err = %q, want it to contain %q", c.name, err, w)
			}
		}
	}
}

func TestMerge(t *testing.T) {
	base := map[string]any{
		"keybinds": map[string]any{"leader": "ctrl+x", "session_delete": "ctrl+d"},
		"theme":    "system",
		"list":     []any{"a", "b"},
	}
	over := map[string]any{
		"keybinds": map[string]any{"session_delete": "ctrl+backspace"},
		"list":     []any{"c"},
		"mouse":    false,
	}
	got := Merge(base, over)
	want := map[string]any{
		"keybinds": map[string]any{"leader": "ctrl+x", "session_delete": "ctrl+backspace"},
		"theme":    "system",
		"list":     []any{"c"},
		"mouse":    false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Merge = %v, want %v", got, want)
	}
	if base["keybinds"].(map[string]any)["session_delete"] != "ctrl+d" {
		t.Error("Merge changed base")
	}
}

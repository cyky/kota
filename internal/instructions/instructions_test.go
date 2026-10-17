package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestConfigHome(t *testing.T) {
	for _, c := range []struct{ xdg, want string }{
		{"", "/home/u/.config"},
		{"relative/dir", "/home/u/.config"},
		{"/xdg", "/xdg"},
	} {
		getenv := func(k string) string {
			if k == "XDG_CONFIG_HOME" {
				return c.xdg
			}
			return ""
		}
		if got := ConfigHome(getenv, "/home/u"); got != c.want {
			t.Errorf("XDG_CONFIG_HOME=%q: ConfigHome = %q, want %q", c.xdg, got, c.want)
		}
		if got, want := OverridePath(getenv, "/home/u"), filepath.Join(c.want, "kota", "instructions.md"); got != want {
			t.Errorf("XDG_CONFIG_HOME=%q: OverridePath = %q, want %q", c.xdg, got, want)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "instructions.md")
	builtin := []byte("# Built in\n")

	s, err := Load(file, builtin)
	if err != nil || s.Text != "# Built in\n" || s.Source != DefaultSource {
		t.Errorf("missing override: %+v, %v; want the built-in style", s, err)
	}

	if err := os.WriteFile(file, []byte("# Mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err = Load(file, builtin)
	if err != nil || s.Text != "# Mine\n" || s.Source != "override: "+file {
		t.Errorf("override: %+v, %v", s, err)
	}
}

func TestLoadRefuses(t *testing.T) {
	cases := map[string]func(t *testing.T, file string){
		"directory": func(t *testing.T, file string) {
			if err := os.Mkdir(file, 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"too large": func(t *testing.T, file string) {
			if err := os.WriteFile(file, []byte(strings.Repeat("x", MaxOverride+1)), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"fifo": func(t *testing.T, file string) {
			if err := syscall.Mkfifo(file, 0o644); err != nil {
				t.Skip("mkfifo:", err)
			}
		},
		"unreadable": func(t *testing.T, file string) {
			if os.Getuid() == 0 {
				t.Skip("root reads anything")
			}
			if err := os.WriteFile(file, []byte("x"), 0o000); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "instructions.md")
			setup(t, file)
			_, err := Load(file, []byte("x"))
			if err == nil || !strings.Contains(err.Error(), file) {
				t.Errorf("err = %v, want an error naming %s", err, file)
			}
		})
	}
}

func TestNote(t *testing.T) {
	style := Style{Text: "# Working on tasks\n\n- one\n"}
	got := Note(style, nil, "")
	if !strings.HasPrefix(got, "# Working on tasks\n\n- one\n\n# This sandbox\n") {
		t.Errorf("note doesn't start with the style, then the Sandbox facts:\n%s", got)
	}
	for _, want := range []string{"offline", "go doc", "node_modules", "kota prep", "don't add it to the manifest", "unattended"} {
		if !strings.Contains(got, want) {
			t.Errorf("Sandbox facts lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\n\n\n") || !strings.HasSuffix(got, "\n") || strings.HasSuffix(got, "\n\n") {
		t.Errorf("note spacing:\n%q", got)
	}

	withEngine := Note(style, nil, "# Containers\n\nengine text\n")
	if !strings.HasPrefix(withEngine, strings.TrimSuffix(got, "\n")+"\n\n# Containers\n\nengine text\n") {
		t.Errorf("Engine VM note not appended after the Sandbox facts:\n%s", withEngine)
	}

	withFacts := Note(style, []string{"Call ticket_done."}, "# Containers\n\nengine text\n")
	if !strings.Contains(withFacts, "park it.\n- Call ticket_done.\n\n# Containers\n") {
		t.Errorf("agent facts not appended to the Sandbox facts, before the Engine VM note:\n%s", withFacts)
	}

	empty := Note(Style{Text: "  \n"}, nil, "")
	if !strings.HasPrefix(empty, "# This sandbox\n") {
		t.Errorf("empty style: note = %q, want only the Sandbox facts", empty)
	}
}

package projectskills

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		want    bool
		wantErr string
	}{
		{name: "empty file", yaml: ""},
		{name: "no skills key", yaml: "prep:\n  command: make\n"},
		{name: "on", yaml: "skills: true\n", want: true},
		{name: "off", yaml: "skills: false\n"},
		{name: "null", yaml: "skills:\n"},
		{name: "with other sections", yaml: "prep:\n  dirs: [.venv]\nskills: true\nengine:\n  images: [redis]\n", want: true},
		{name: "alias", yaml: "x: &on true\nskills: *on\n", want: true},
		{name: "yes is a string", yaml: "skills: yes\n", wantErr: `skills: want true or false, got "yes"`},
		{name: "quoted", yaml: "skills: \"true\"\n", wantErr: "want true or false"},
		{name: "read-only", yaml: "skills: read-only\n", wantErr: "want true or false"},
		{name: "mapping", yaml: "skills:\n  writable: true\n", wantErr: "want true or false"},
		{name: "list", yaml: "skills: [a]\n", wantErr: "want true or false"},
		{name: "broken yaml", yaml: "skills: [\n", wantErr: "parse kota.yaml"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Parse([]byte(c.yaml))
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("Parse = %v, want %v", got, c.want)
			}
		})
	}
}

func TestRead(t *testing.T) {
	cases := []struct {
		name     string
		config   string // "" for no kota.yaml
		writable bool
		want     Mode
		wantErr  string
		// configErr: the error is a *ConfigError.
		configErr bool
	}{
		{name: "no config", want: Off},
		{name: "no config, writable", writable: true, wantErr: "--skills-writable needs skills: true in kota.yaml"},
		{name: "off", config: "skills: false\n", want: Off},
		{name: "off, writable", config: "skills: false\n", writable: true, wantErr: "--skills-writable needs skills: true"},
		{name: "on", config: "skills: true\n", want: ReadOnly},
		{name: "on, writable", config: "skills: true\n", writable: true, want: Writable},
		{name: "bad value", config: "skills: writable\n", wantErr: "want true or false", configErr: true},
		{name: "broken yaml", config: "prep: [\n", wantErr: "parse kota.yaml", configErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			project := t.TempDir()
			if c.config != "" {
				if err := os.WriteFile(filepath.Join(project, ConfigFile), []byte(c.config), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := Read(project, c.writable)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, c.wantErr)
				}
				var cfgErr *ConfigError
				if errors.As(err, &cfgErr) != c.configErr {
					t.Errorf("err = %#v, want a *ConfigError: %v", err, c.configErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("Read = %v, want %v", got, c.want)
			}
		})
	}
}

func TestCheckLinks(t *testing.T) {
	cases := []struct {
		name string
		// setup builds the project; dir is the project root.
		setup   func(t *testing.T, dir string)
		wantErr string
	}{
		{name: "no folder", setup: func(*testing.T, string) {}},
		{name: "plain skills", setup: func(t *testing.T, dir string) {
			write(t, dir, Dir+"/deploy/SKILL.md")
		}},
		{name: "link inside the folder", setup: func(t *testing.T, dir string) {
			write(t, dir, Dir+"/deploy/SKILL.md")
			link(t, dir, "deploy", Dir+"/ship")
		}},
		{name: "link to a file inside the folder", setup: func(t *testing.T, dir string) {
			write(t, dir, Dir+"/deploy/SKILL.md")
			write(t, dir, Dir+"/ship/notes.md")
			link(t, dir, "../deploy/SKILL.md", Dir+"/ship/SKILL.md")
		}},
		{name: "link elsewhere in the project", setup: func(t *testing.T, dir string) {
			write(t, dir, "tools/skills/deploy/SKILL.md")
			link(t, dir, "../../tools/skills/deploy", Dir+"/deploy")
		}, wantErr: ".agents/skills/deploy is a symlink to ../../tools/skills/deploy, which a read-only mount"},
		{name: "link to a sibling of the folder", setup: func(t *testing.T, dir string) {
			write(t, dir, ".agents/skills-old/x/SKILL.md")
			link(t, dir, "../skills-old/x", Dir+"/x")
		}, wantErr: ".agents/skills/x is a symlink to ../skills-old/x"},
		{name: "link outside the project", setup: func(t *testing.T, dir string) {
			link(t, dir, "/etc", Dir+"/etc")
		}, wantErr: "is a symlink to /etc"},
		{name: "dangling link", setup: func(t *testing.T, dir string) {
			link(t, dir, "../../gone", Dir+"/gone")
		}, wantErr: "is a symlink to ../../gone"},
		{name: "absolute link inside the folder", setup: func(t *testing.T, dir string) {
			write(t, dir, Dir+"/deploy/SKILL.md")
			link(t, dir, filepath.Join(dir, Dir, "deploy"), Dir+"/ship")
		}, wantErr: "/ship is a symlink to /"},
		{name: "nested link", setup: func(t *testing.T, dir string) {
			write(t, dir, "scripts/run.sh")
			write(t, dir, Dir+"/deploy/SKILL.md")
			link(t, dir, "../../../scripts/run.sh", Dir+"/deploy/run.sh")
		}, wantErr: ".agents/skills/deploy/run.sh is a symlink"},
		{name: "folder is a file", setup: func(t *testing.T, dir string) {
			write(t, dir, Dir)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			c.setup(t, dir)
			err := CheckLinks(dir)
			if c.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, c.wantErr)
			}
		})
	}
}

func TestCheckLinksMessage(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "tools/x/SKILL.md")
	link(t, dir, "../../tools/x", Dir+"/x")
	err := CheckLinks(dir)
	// check and prep don't take --skills-writable, so the message must say
	// how to fix the link, and that the flag is for run and shell.
	for _, want := range []string{"replace it with a copy, or with a relative link inside .agents/skills", "For one run or shell session, --skills-writable"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}
}

func TestCheckPath(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, dir string)
		wantErr string
	}{
		{name: "missing", setup: func(*testing.T, string) {}},
		{name: "parent only", setup: func(t *testing.T, dir string) { write(t, dir, ".agents/notes.md") }},
		{name: "folder", setup: func(t *testing.T, dir string) { write(t, dir, Dir+"/a/SKILL.md") }},
		{name: "parent is a link", setup: func(t *testing.T, dir string) {
			write(t, dir, "elsewhere/skills/a/SKILL.md")
			link(t, dir, "elsewhere", ".agents")
		}, wantErr: "refusing to use .agents/skills: .agents is a symlink"},
		{name: "folder is a link", setup: func(t *testing.T, dir string) {
			write(t, dir, "elsewhere/a/SKILL.md")
			link(t, dir, "../elsewhere", Dir)
		}, wantErr: ".agents/skills is a symlink"},
		{name: "parent is a file", setup: func(t *testing.T, dir string) { write(t, dir, ".agents") }, wantErr: ".agents is a file, not a directory"},
		{name: "folder is a file", setup: func(t *testing.T, dir string) { write(t, dir, Dir) }, wantErr: ".agents/skills is a file, not a directory"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			c.setup(t, dir)
			err := CheckPath(dir)
			if c.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, c.wantErr)
			}
		})
	}
}

func TestModeString(t *testing.T) {
	for m, want := range map[Mode]string{
		Off:      "off",
		ReadOnly: "on, read-only (.agents/skills; --skills-writable to let the agent edit them)",
		Writable: "on, writable (.agents/skills; --skills-writable)",
	} {
		if got := m.String(); got != want {
			t.Errorf("%d: %q, want %q", m, got, want)
		}
	}
}

func write(t *testing.T, dir, rel string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func link(t *testing.T, dir, target, rel string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

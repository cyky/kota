package pathpolicy

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"localllm/internal/sandbox"
)

// fixture lays out a fake home with an allowed root, projects, forbidden
// directories and symlinks, and returns its resolved path.
func fixture(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{
		"Projects/app/sub",
		"Projects/other",
		"Projects/specs",
		"Docs/specs",
		"Docs/api",
		"elsewhere",
		".ssh",
		".config/tool",
		".rd",
	} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "Docs/notes.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"Projects/escape":     filepath.Join(home, "elsewhere"),
		"Projects/to-ssh":     filepath.Join(home, ".ssh"),
		"Projects/to-home":    home,
		"Projects/to-app":     filepath.Join(home, "Projects/app"),
		"Docs/ssh-link":       filepath.Join(home, ".ssh"),
		"Docs/socket-link":    filepath.Join(home, ".rd"),
		"Docs/specs-link":     filepath.Join(home, "Docs/specs"),
		"Projects/app/to-sub": filepath.Join(home, "Projects/app/sub"),
	}
	for link, dest := range links {
		if err := os.Symlink(dest, filepath.Join(home, link)); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func testPolicy(home string) Policy {
	p := Default(home, "")
	p.CaseInsensitive = false
	return p
}

func TestValidateProject(t *testing.T) {
	home := fixture(t)
	tests := []struct {
		name    string
		project string
		want    string // resolved project; empty when refused
		refusal string // substring of the refusal
	}{
		{name: "inside root", project: "Projects/app", want: "Projects/app"},
		{name: "nested inside root", project: "Projects/app/sub", want: "Projects/app/sub"},
		{name: "symlink inside root", project: "Projects/to-app", want: "Projects/app"},
		{name: "unclean path", project: "Projects/other/../app/./", want: "Projects/app"},
		{name: "home", project: ".", refusal: "home directory"},
		{name: "filesystem root", project: "/", refusal: "filesystem root"},
		{name: "allowed root itself", project: "Projects", refusal: "allowed root itself"},
		{name: "outside root", project: "elsewhere", refusal: "outside the allowed root"},
		{name: "symlink escape", project: "Projects/escape", refusal: "outside the allowed root"},
		{name: "symlink to home", project: "Projects/to-home", refusal: "home directory"},
		{name: "symlink to ssh", project: "Projects/to-ssh", refusal: "outside the allowed root"},
		{name: "forbidden", project: ".ssh", refusal: "outside the allowed root"},
		{name: "missing", project: "Projects/nope", refusal: "no such file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := tt.project
			if !filepath.IsAbs(project) {
				project = filepath.Join(home, project)
			}
			got, err := Validate(project, nil, testPolicy(home))
			if tt.refusal != "" {
				if err == nil || !strings.Contains(err.Error(), tt.refusal) {
					t.Fatalf("err = %v, want refusal containing %q", err, tt.refusal)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(home, tt.want); got.Project != want {
				t.Errorf("project = %s, want %s", got.Project, want)
			}
		})
	}
}

func TestValidateReadOnly(t *testing.T) {
	home := fixture(t)
	tests := []struct {
		name    string
		ro      []string
		want    []sandbox.Mount // sources relative to home
		refusal string
	}{
		{name: "none"},
		{
			name: "outside allowed root is fine",
			ro:   []string{"Docs/api", "Docs/notes.md"},
			want: []sandbox.Mount{{Source: "Docs/api", Target: "/ro/api"}, {Source: "Docs/notes.md", Target: "/ro/notes.md"}},
		},
		{
			name: "other project",
			ro:   []string{"Projects/other"},
			want: []sandbox.Mount{{Source: "Projects/other", Target: "/ro/other"}},
		},
		{
			name: "duplicates after resolving",
			ro:   []string{"Docs/specs", "Docs/specs-link", "Docs/specs/"},
			want: []sandbox.Mount{{Source: "Docs/specs", Target: "/ro/specs"}},
		},
		{
			name: "same basename",
			ro:   []string{"Docs/specs", "Projects/specs"},
			want: []sandbox.Mount{{Source: "Docs/specs", Target: "/ro/specs"}, {Source: "Projects/specs", Target: "/ro/specs-2"}},
		},
		{
			name: "nested extras",
			ro:   []string{"Docs", "Docs/api"},
			want: []sandbox.Mount{{Source: "Docs", Target: "/ro/Docs"}, {Source: "Docs/api", Target: "/ro/api"}},
		},
		{name: "ssh", ro: []string{".ssh"}, refusal: "never mounted"},
		{name: "inside forbidden", ro: []string{".config/tool"}, refusal: "never mounted"},
		{name: "symlink to ssh", ro: []string{"Docs/ssh-link"}, refusal: "never mounted"},
		{name: "symlink to socket dir", ro: []string{"Docs/socket-link"}, refusal: "never mounted"},
		{name: "home", ro: []string{"."}, refusal: "contains your home"},
		{name: "filesystem root", ro: []string{"/"}, refusal: "contains your home"},
		{name: "parent of home", ro: []string{".."}, refusal: "contains your home"},
		{name: "project itself", ro: []string{"Projects/app"}, refusal: "overlaps the project"},
		{name: "inside project", ro: []string{"Projects/app/sub"}, refusal: "overlaps the project"},
		{name: "symlink into project", ro: []string{"Projects/app/to-sub"}, refusal: "overlaps the project"},
		{name: "contains project", ro: []string{"Projects"}, refusal: "overlaps the project"},
		{name: "one bad extra refuses all", ro: []string{"Docs/api", ".ssh"}, refusal: "never mounted"},
		{name: "missing", ro: []string{"Docs/nope"}, refusal: "no such file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ro []string
			for _, r := range tt.ro {
				if !filepath.IsAbs(r) {
					r = filepath.Join(home, r)
				}
				ro = append(ro, r)
			}
			got, err := Validate(filepath.Join(home, "Projects/app"), ro, testPolicy(home))
			if tt.refusal != "" {
				if err == nil || !strings.Contains(err.Error(), tt.refusal) {
					t.Fatalf("err = %v, want refusal containing %q", err, tt.refusal)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var want []sandbox.Mount
			for _, m := range tt.want {
				want = append(want, sandbox.Mount{Source: filepath.Join(home, m.Source), Target: m.Target})
			}
			if !slices.Equal(got.ReadOnly, want) {
				t.Errorf("read-only = %+v, want %+v", got.ReadOnly, want)
			}
		})
	}
}

func TestForbiddenSocketPath(t *testing.T) {
	home := fixture(t)
	sock := filepath.Join(home, "Docs/docker.sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p := testPolicy(home)
	p.Forbidden = append(p.Forbidden, sock)

	for _, ro := range []string{sock, filepath.Join(home, "Docs")} {
		if _, err := Validate(filepath.Join(home, "Projects/app"), []string{ro}, p); err == nil {
			t.Errorf("--ro %s accepted although it overlaps the socket", ro)
		}
	}
}

func TestForbiddenMatchedThroughSymlink(t *testing.T) {
	// A forbidden entry that is itself a link (like /var/run/docker.sock ->
	// ~/.rd/docker.sock) is refused under its resolved name too.
	home := fixture(t)
	link := filepath.Join(home, "Docs/run-link")
	if err := os.Symlink(filepath.Join(home, "elsewhere"), link); err != nil {
		t.Fatal(err)
	}
	p := testPolicy(home)
	p.Forbidden = append(p.Forbidden, link)

	if _, err := Validate(filepath.Join(home, "Projects/app"), []string{filepath.Join(home, "elsewhere")}, p); err == nil {
		t.Error("resolved target of a forbidden link was accepted")
	}
}

func TestCaseInsensitive(t *testing.T) {
	home := fixture(t)
	p := testPolicy(home)
	p.CaseInsensitive = true
	// On a case-sensitive filesystem the differently cased name doesn't
	// exist, so feed the policy a path that does but under a forbidden name
	// with different case.
	p.Forbidden = append(p.Forbidden, filepath.Join(home, "DOCS"))

	if _, err := Validate(filepath.Join(home, "Projects/app"), []string{filepath.Join(home, "Docs/api")}, p); err == nil {
		t.Error("case-insensitive policy accepted a path under a forbidden name of different case")
	}
	p.CaseInsensitive = false
	if _, err := Validate(filepath.Join(home, "Projects/app"), []string{filepath.Join(home, "Docs/api")}, p); err != nil {
		t.Errorf("case-sensitive policy refused: %v", err)
	}
}

func TestAllowedRootIsConfigurable(t *testing.T) {
	home := fixture(t)
	p := Default(home, filepath.Join(home, "elsewhere"))
	p.CaseInsensitive = false
	if err := os.Mkdir(filepath.Join(home, "elsewhere/proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(filepath.Join(home, "elsewhere/proj"), nil, p); err != nil {
		t.Errorf("project inside configured root refused: %v", err)
	}
	if _, err := Validate(filepath.Join(home, "Projects/app"), nil, p); err == nil {
		t.Error("project outside configured root accepted")
	}
	p.AllowedRoot = filepath.Join(home, "missing")
	if _, err := Validate(filepath.Join(home, "Projects/app"), nil, p); err == nil || !strings.Contains(err.Error(), "LOCALLLM_ALLOWED_ROOT") {
		t.Errorf("missing allowed root: err = %v", err)
	}
}

func TestRefusalNamesResolvedPath(t *testing.T) {
	home := fixture(t)
	_, err := Validate(filepath.Join(home, "Projects/escape"), nil, testPolicy(home))
	if err == nil || !strings.Contains(err.Error(), "resolves to "+filepath.Join(home, "elsewhere")) {
		t.Errorf("err = %v", err)
	}
}

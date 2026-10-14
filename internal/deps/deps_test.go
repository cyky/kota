package deps

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const store = "/store"

// project creates a project from a map of relative path to content.
func project(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func detect(t *testing.T, files map[string]string) Plan {
	t.Helper()
	plan, err := Detect(project(t, files), Options{PnpmStore: store})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func scripts(p Plan) []string {
	var out []string
	for _, s := range p.Steps {
		out = append(out, s.Script)
	}
	return out
}

func dirs(p Plan) []string {
	var out []string
	for _, d := range p.Dirs {
		out = append(out, d.Path)
	}
	return out
}

func TestDetectPackageManager(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"nothing", map[string]string{"README.md": ""}, nil},
		{"npm without lockfile", map[string]string{"package.json": "{}"}, []string{"npm install"}},
		{"npm lockfile", map[string]string{"package.json": "{}", "package-lock.json": "{}"}, []string{"npm ci"}},
		{"npm shrinkwrap", map[string]string{"package.json": "{}", "npm-shrinkwrap.json": "{}"}, []string{"npm ci"}},
		{"pnpm lockfile", map[string]string{"package.json": "{}", "pnpm-lock.yaml": ""}, []string{"pnpm install --store-dir /store --frozen-lockfile"}},
		{"pnpm field without lockfile", map[string]string{"package.json": `{"packageManager":"pnpm@9.12.0"}`}, []string{"pnpm install --store-dir /store"}},
		{"field beats lockfile", map[string]string{"package.json": `{"packageManager":"pnpm@9.12.0+sha512.abc"}`, "package-lock.json": "{}"}, []string{"pnpm install --store-dir /store"}},
		{"yarn classic", map[string]string{"package.json": "{}", "yarn.lock": ""}, []string{"yarn install --frozen-lockfile"}},
		{"yarn 1 field", map[string]string{"package.json": `{"packageManager":"yarn@1.22.22"}`, "yarn.lock": ""}, []string{"yarn install --frozen-lockfile"}},
		{"yarn berry field", map[string]string{"package.json": `{"packageManager":"yarn@4.5.0"}`, "yarn.lock": ""}, []string{"yarn install --immutable"}},
		{"yarn berry yarnrc", map[string]string{"package.json": "{}", "yarn.lock": "", ".yarnrc.yml": ""}, []string{"yarn install --immutable"}},
		{"go", map[string]string{"go.mod": "module x\n"}, []string{"go mod download"}},
		{"go nested modules", map[string]string{"go.mod": "module x\n", "tools/go.mod": "module t\n", "testdata/go.mod": "", "vendor/a/go.mod": ""}, []string{"go mod download", "(cd 'tools' && go mod download)"}},
		{"go workspace", map[string]string{"go.work": "go 1.27\n", "a/go.mod": "module a\n", "b/go.mod": "module b\n"}, []string{"go mod download"}},
		{"node and go", map[string]string{"package.json": "{}", "pnpm-lock.yaml": "", "go.mod": "module x\n"}, []string{"pnpm install --store-dir /store --frozen-lockfile", "go mod download"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := scripts(detect(t, c.files)); !slices.Equal(got, c.want) {
				t.Errorf("steps = %q, want %q", got, c.want)
			}
		})
	}
}

func TestDetectUnsupportedManager(t *testing.T) {
	_, err := Detect(project(t, map[string]string{"package.json": `{"packageManager":"bun@1.1.0"}`}), Options{})
	if err == nil || !strings.Contains(err.Error(), ConfigFile) {
		t.Errorf("err = %v, want a pointer to %s", err, ConfigFile)
	}
}

func TestDependencyDirs(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"go has none", map[string]string{"go.mod": "module x\n"}, nil},
		{"root package", map[string]string{"package.json": "{}", "fixtures/x/package.json": "{}"}, []string{"node_modules"}},
		{
			"pnpm workspace",
			map[string]string{
				"package.json":                             "{}",
				"pnpm-lock.yaml":                           "",
				"pnpm-workspace.yaml":                      "packages:\n  - 'packages/*'\n  - 'apps/**'\n  - '!apps/legacy'\n",
				"packages/a/package.json":                  "{}",
				"packages/b/package.json":                  "{}",
				"packages/b/node_modules/dep/package.json": "{}",
				"apps/web/package.json":                    "{}",
				"apps/web/sub/package.json":                "{}",
				"apps/legacy/package.json":                 "{}",
				"other/package.json":                       "{}",
			},
			[]string{"node_modules", "apps/web/node_modules", "apps/web/sub/node_modules", "packages/a/node_modules", "packages/b/node_modules"},
		},
		{
			"npm workspaces list",
			map[string]string{"package.json": `{"workspaces":["pkgs/*"]}`, "pkgs/x/package.json": "{}"},
			[]string{"node_modules", "pkgs/x/node_modules"},
		},
		{
			"yarn workspaces object",
			map[string]string{"package.json": `{"workspaces":{"packages":["./pkgs/*"]}}`, "yarn.lock": "", "pkgs/y/package.json": "{}"},
			[]string{"node_modules", "pkgs/y/node_modules"},
		},
		{
			"config adds dirs",
			map[string]string{"package.json": "{}", ConfigFile: "prep:\n  dirs: [.venv, node_modules/, py/.venv]\n"},
			[]string{"node_modules", ".venv", "py/.venv"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := dirs(detect(t, c.files))
			slices.Sort(got)
			want := slices.Clone(c.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("dirs = %q, want %q", got, c.want)
			}
		})
	}
}

func TestConfigCommandReplacesSteps(t *testing.T) {
	plan := detect(t, map[string]string{
		"package.json": "{}",
		"go.mod":       "module x\n",
		ConfigFile:     "prep:\n  command: |\n    make deps\n",
	})
	if got := scripts(plan); !slices.Equal(got, []string{"make deps"}) {
		t.Errorf("steps = %q", got)
	}
	if !slices.Contains(dirs(plan), "node_modules") {
		t.Errorf("detected dirs dropped: %q", dirs(plan))
	}
}

func TestConfigErrors(t *testing.T) {
	cases := map[string]string{
		"unknown key":  "prep:\n  comand: x\n",
		"absolute dir": "prep:\n  dirs: [/tmp/x]\n",
		"escaping dir": "prep:\n  dirs: [../x]\n",
		"root dir":     "prep:\n  dirs: [.]\n",
		"git dir":      "prep:\n  dirs: [.git/hooks]\n",
		"not yaml":     "prep: [\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Detect(project(t, map[string]string{ConfigFile: content}), Options{}); err == nil {
				t.Error("no error")
			}
		})
	}
	if _, err := Detect(project(t, map[string]string{ConfigFile: ""}), Options{}); err != nil {
		t.Errorf("empty config: %v", err)
	}
}

func TestSymlinkedDependencyDirRefused(t *testing.T) {
	dir := project(t, map[string]string{"package.json": "{}"})
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "node_modules")); err != nil {
		t.Fatal(err)
	}
	if _, err := Detect(dir, Options{}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("err = %v, want symlink refusal", err)
	}
}

func TestConflict(t *testing.T) {
	plan := Plan{Dirs: []Dir{{Path: "node_modules"}, {Path: "tools/.venv"}}}
	for _, paths := range [][]string{{"node_modules"}, {"tools"}, {"tools/.venv/x"}} {
		if plan.Conflict(paths, nil) == nil {
			t.Errorf("no conflict with protected %q", paths)
		}
	}
	for _, paths := range [][]string{{"node_modules"}, {"tools/.venv/x"}} {
		if plan.Conflict(nil, paths) == nil {
			t.Errorf("no conflict with pinned %q", paths)
		}
	}
	if err := plan.Conflict([]string{".git", ".envrc", "tool", "node_modules2"}, []string{"tools"}); err != nil {
		t.Errorf("unexpected conflict: %v", err)
	}
}

func TestScriptStopsOnFailure(t *testing.T) {
	s := Plan{Steps: []Step{{Name: "a", Script: "echo 'x'"}, {Name: "b", Script: "true"}}}.Script()
	if !strings.HasPrefix(s, "set -e\n") || !strings.Contains(s, "echo 'x'\n") || !strings.Contains(s, `'==> a: echo '\''x'\'''`) {
		t.Errorf("script = %q", s)
	}
}

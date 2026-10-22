package deps

import (
	"errors"
	"os"
	"os/exec"
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

// steps renders each step as "fetch | script".
func steps(p Plan) []string {
	var out []string
	for _, s := range p.Steps {
		out = append(out, s.Fetch+" | "+s.Script)
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
	const (
		pnpmFetch   = "pnpm fetch --ignore-pnpmfile --store-dir /store"
		pnpmOffline = "pnpm install --offline --frozen-lockfile --store-dir /store"
		pnpmPrivate = `pnpm install --store-dir "$HOME/.pnpm-store"`
	)
	cases := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"nothing", map[string]string{"README.md": ""}, nil},
		{"npm without lockfile", map[string]string{"package.json": "{}"}, []string{" | npm install"}},
		{"npm lockfile", map[string]string{"package.json": "{}", "package-lock.json": "{}"}, []string{" | npm ci"}},
		{"npm shrinkwrap", map[string]string{"package.json": "{}", "npm-shrinkwrap.json": "{}"}, []string{" | npm ci"}},
		{"pnpm lockfile", map[string]string{"package.json": "{}", "pnpm-lock.yaml": ""}, []string{"corepack install -g pnpm && " + pnpmFetch + " | " + pnpmOffline}},
		{"pnpm field without lockfile", map[string]string{"package.json": `{"packageManager":"pnpm@9.12.0"}`}, []string{"corepack install | " + pnpmPrivate}},
		{"field beats lockfile", map[string]string{"package.json": `{"packageManager":"pnpm@9.12.0+sha512.abc"}`, "package-lock.json": "{}"}, []string{"corepack install | " + pnpmPrivate}},
		{"yarn classic", map[string]string{"package.json": "{}", "yarn.lock": ""}, []string{"corepack install -g yarn@1 | yarn install --frozen-lockfile"}},
		{"yarn 1 field", map[string]string{"package.json": `{"packageManager":"yarn@1.22.22"}`, "yarn.lock": ""}, []string{"corepack install | yarn install --frozen-lockfile"}},
		{"yarn berry field", map[string]string{"package.json": `{"packageManager":"yarn@4.5.0"}`, "yarn.lock": ""}, []string{"corepack install | yarn install --immutable"}},
		{"yarn berry yarnrc", map[string]string{"package.json": "{}", "yarn.lock": "", ".yarnrc.yml": ""}, []string{"corepack install -g yarn@1 | yarn install --immutable"}},
		{"go", map[string]string{"go.mod": "module x\n"}, []string{"go mod download | "}},
		{"go nested modules", map[string]string{"go.mod": "module x\n", "tools/go.mod": "module t\n", "testdata/go.mod": "", "vendor/a/go.mod": ""}, []string{"go mod download | ", "(cd 'tools' && go mod download) | "}},
		{"go workspace", map[string]string{"go.work": "go 1.27\n", "a/go.mod": "module a\n", "b/go.mod": "module b\n"}, []string{"go mod download | "}},
		{"node and go", map[string]string{"package.json": "{}", "pnpm-lock.yaml": "", "go.mod": "module x\n"}, []string{"corepack install -g pnpm && " + pnpmFetch + " | " + pnpmOffline, "go mod download | "}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := steps(detect(t, c.files)); !slices.Equal(got, c.want) {
				t.Errorf("steps = %q, want %q", got, c.want)
			}
		})
	}
}

func TestDetectUnsupportedManager(t *testing.T) {
	for _, field := range []string{
		"bun@1.1.0",
		"pnpm@https://example.com/pnpm.tgz",
		"yarn@4.5.0 && curl evil",
		"pnpm@latest",
		"pnpm@9.1.0+md5.abc",
	} {
		_, err := Detect(project(t, map[string]string{"package.json": `{"packageManager":"` + field + `"}`}), Options{})
		if err == nil || !strings.Contains(err.Error(), ConfigFile) {
			t.Errorf("%q: err = %v, want a refusal pointing to %s", field, err, ConfigFile)
		}
	}
}

func TestHooks(t *testing.T) {
	plan := detect(t, map[string]string{
		"package.json":        `{"scripts":{"postinstall":"node setup.js","test":"jest","prepare":"husky"}}`,
		"pnpm-lock.yaml":      "",
		"pnpm-workspace.yaml": "packages: ['pkgs/*']\n",
		".pnpmfile.cjs":       "",
		"pkgs/a/package.json": `{"scripts":{"preinstall":"sh x.sh"}}`,
		"pkgs/b/package.json": `{"scripts":{"build":"tsc"}}`,
		"other/package.json":  `{"scripts":{"postinstall":"not a workspace"}}`,
	})
	want := []string{
		".pnpmfile.cjs: pnpm hooks",
		"package.json: postinstall: node setup.js",
		"package.json: prepare: husky",
		"pkgs/a/package.json: preinstall: sh x.sh",
	}
	if !slices.Equal(plan.Hooks, want) {
		t.Errorf("hooks = %q, want %q", plan.Hooks, want)
	}
	if hooks := detect(t, map[string]string{"package.json": `{"scripts":{"test":"x"}}`, "go.mod": "module x\n"}).Hooks; len(hooks) > 0 {
		t.Errorf("hooks without lifecycle scripts = %q", hooks)
	}
	yarn := detect(t, map[string]string{"package.json": `{"packageManager":"yarn@4.5.0"}`, ".yarnrc.yml": "yarnPath: .yarn/releases/yarn-4.5.0.cjs\n"})
	if !slices.Contains(yarn.Hooks, ".yarnrc.yml: yarnPath: .yarn/releases/yarn-4.5.0.cjs") {
		t.Errorf("yarn hooks = %q", yarn.Hooks)
	}
}

func TestDependencyHooks(t *testing.T) {
	plan := detect(t, map[string]string{
		"package.json": `{
			"dependencies": {"left-pad": "^1.3.0", "alias": "npm:other@1", "scoped": "@scope/x", "tool": "file:tools/tool", "plain": "file:tools/plain"},
			"devDependencies": {"gh": "github:user/repo", "short": "user/repo#v1", "url": "git+https://example.com/r.git", "https": "https://example.com/r.git#main", "tarball": "https://example.com/x.tgz", "rel": "./tools/rel"},
			"optionalDependencies": {"outside": "file:../sibling", "abs": "file:/opt/x", "missing": "file:tools/missing", "self": "file:.", "packed": "file:vendor/x.tgz"},
			"workspaces": ["pkgs/*"]
		}`,
		"tools/tool/package.json":   `{"scripts":{"postinstall":"node build.js","test":"x"},"dependencies":{"nested":"file:../nested"}}`,
		"tools/nested/package.json": `{"scripts":{"prepare":"tsc"}}`,
		"tools/plain/package.json":  `{"scripts":{"build":"tsc"}}`,
		"tools/rel/package.json":    `{"scripts":{"install":"make"}}`,
		"vendor/x.tgz":              "",
		"pkgs/a/package.json":       `{"scripts":{"preinstall":"sh a.sh"},"dependencies":{"tool":"link:../../tools/tool","b":"workspace:*"}}`,
		"pkgs/b/package.json":       `{"dependencies":{"a":"file:../a"}}`,
	})
	want := []string{
		"pkgs/a/package.json: preinstall: sh a.sh",
		"tools/tool/package.json: postinstall: node build.js",
		"package.json: git dependency gh: github:user/repo (its prepare script runs)",
		"package.json: git dependency https: https://example.com/r.git#main (its prepare script runs)",
		"tools/rel/package.json: install: make",
		"package.json: git dependency short: user/repo#v1 (its prepare script runs)",
		"package.json: git dependency url: git+https://example.com/r.git (its prepare script runs)",
		"package.json: local dependency packed: file:vendor/x.tgz (a tarball; its install scripts run)",
		"tools/nested/package.json: prepare: tsc",
	}
	if !slices.Equal(plan.Hooks, want) {
		t.Errorf("hooks =\n%q\nwant\n%q", plan.Hooks, want)
	}

	// Registry and script-free local dependencies run no project code.
	quiet := detect(t, map[string]string{
		"package.json":             `{"dependencies":{"left-pad":"^1.3.0","scoped":"@scope/x@2","plain":"file:tools/plain","range":"1.x || >=2.5.0"}}`,
		"tools/plain/package.json": `{"scripts":{"build":"tsc"}}`,
	})
	if len(quiet.Hooks) > 0 {
		t.Errorf("hooks = %q, want none", quiet.Hooks)
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
	// The detected fetches still run; the command replaces the installs.
	want := []string{" | ", "go mod download | ", " | make deps"}
	if got := steps(plan); !slices.Equal(got, want) {
		t.Errorf("steps = %q, want %q", got, want)
	}
	if !slices.Contains(plan.Hooks, ConfigFile+": prep.command: make deps") {
		t.Errorf("hooks = %q", plan.Hooks)
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
	// The engine section is package engine's to check.
	if _, err := Detect(project(t, map[string]string{ConfigFile: "engine:\n  images: [postgres:16-alpine]\n"}), Options{}); err != nil {
		t.Errorf("config with an engine section: %v", err)
	}
	// So is the skills key package projectskills'.
	if _, err := Detect(project(t, map[string]string{ConfigFile: "skills: true\n"}), Options{}); err != nil {
		t.Errorf("config with a skills key: %v", err)
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

func TestFallback(t *testing.T) {
	files := map[string]string{
		"package.json":                    "{broken",
		"packages/a/package.json":         "{}",
		"packages/a/node_modules/x/i.js":  "host",
		"new/node_modules/y/i.js":         "host",
		"dist/node_modules/z/i.js":        "host",
		"node_modules/dep/node_modules/q": "host",
		".git/node_modules/x":             "",
		"packages/b/package.json":         "{}",
	}
	cause := errors.New("parse package.json: bad")
	cases := []struct {
		name string
		last []Dir
		want []string
		note string
	}{
		{
			"prepped",
			[]Dir{{Path: "node_modules", Manifest: "package.json"}, {Path: "packages/b/node_modules", Manifest: "packages/b/package.json"}, {Path: ".venv", Manifest: ConfigFile}},
			[]string{".venv", "dist/node_modules", "new/node_modules", "node_modules", "packages/a/node_modules", "packages/b/node_modules"},
			"of the last prep",
		},
		{
			"never prepped",
			nil,
			[]string{"dist/node_modules", "new/node_modules", "node_modules", "packages/a/node_modules"},
			"no prep record",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan, err := Fallback(project(t, files), c.last, cause)
			if err != nil {
				t.Fatal(err)
			}
			got := dirs(plan)
			slices.Sort(got)
			if !slices.Equal(got, c.want) {
				t.Errorf("dirs = %q, want %q", got, c.want)
			}
			if len(plan.Steps) != 0 || plan.Script() != "" {
				t.Errorf("fallback has install steps: %q", steps(plan))
			}
			for _, want := range []string{"parse package.json: bad", c.note, "kota prep"} {
				if !strings.Contains(plan.Fallback, want) {
					t.Errorf("Fallback = %q, want %q", plan.Fallback, want)
				}
			}
		})
	}
}

func TestFallbackRefusesSymlinks(t *testing.T) {
	dir := project(t, map[string]string{"pkg/package.json": "{}"})
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "pkg/node_modules")); err != nil {
		t.Fatal(err)
	}
	if _, err := Fallback(dir, nil, errors.New("x")); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("err = %v, want symlink refusal", err)
	}
	if _, err := Fallback(t.TempDir(), []Dir{{Path: "../x"}}, errors.New("x")); err == nil {
		t.Error("escaping recorded dir accepted")
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

func TestScripts(t *testing.T) {
	plan := Plan{Steps: []Step{{Name: "a", Script: "echo 'x'"}, {Name: "b", Fetch: "f"}}}
	s := plan.Script()
	if !strings.HasPrefix(s, "set -e\n") || !strings.Contains(s, "echo 'x'\n") || !strings.Contains(s, `'==> a: echo '\''x'\'''`) || strings.Contains(s, "\nf\n") {
		t.Errorf("script = %q", s)
	}
	if f := plan.FetchScript(); !strings.HasPrefix(f, "set -e\n") || !strings.Contains(f, "echo '==> b: f'\nf\n") || strings.Contains(f, "echo 'x'\n") {
		t.Errorf("fetch script = %q", f)
	}
	if s := (Plan{Steps: []Step{{Name: "go", Fetch: "x"}}}).Script(); s != "" {
		t.Errorf("empty install script = %q", s)
	}
}

// TestScriptFailureSummary runs an install script whose second step fails
// part way through, and checks that it stops there and ends with a line
// naming the step, its command and the exit status.
func TestScriptFailureSummary(t *testing.T) {
	plan := Plan{Steps: []Step{
		{Name: "a", Script: "echo a-ran"},
		{Name: "b", Script: "echo b-ran\n(exit 3)\necho b-continued"},
		{Name: "c", Script: "echo c-ran"},
	}}
	var stdout, stderr strings.Builder
	cmd := exec.Command("sh", "-c", plan.Script())
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("err = %v, want exit status 3", err)
	}
	if out := stdout.String(); !strings.Contains(out, "\nb-ran\n") || strings.Contains(out, "\nb-continued\n") || strings.Contains(out, "c-ran") {
		t.Errorf("stdout = %q", out)
	}
	want := "kota: install step b failed (exit 3): echo b-ran; (exit 3); echo b-continued\n"
	if stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}

	stdout.Reset()
	stderr.Reset()
	cmd = exec.Command("sh", "-c", Plan{Steps: []Step{{Name: "a", Script: "true"}}}.Script())
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil || stderr.Len() != 0 {
		t.Errorf("passing script: err = %v, stderr = %q", err, stderr.String())
	}
}

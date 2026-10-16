package mask

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestExcludes(t *testing.T) {
	l := Layout{Mounts: []Mount{
		{Path: ".envrc", Placeholder: true},
		{Path: ".env", Mask: true},
		{Path: "deploy/.aws", Dir: true, Mask: true},
		{Path: "kota", Dir: true, Placeholder: true},
		{Path: ".git", Dir: true, Pin: true},
		{Path: ".git/hooks", Dir: true, Placeholder: true},
		{Path: "package.json"},
		{Path: "a\nb", Mask: true},
	}}
	want := "/.envrc\n/.env\n/deploy/.aws/\n/kota/\n"
	if got := l.Excludes(); got != want {
		t.Errorf("Excludes() = %q, want %q", got, want)
	}
}

// TestExcludesMatchGit checks that each excluded path is ignored by git as
// itself only, special characters included.
func TestExcludesMatchGit(t *testing.T) {
	isolateGit(t)
	paths := []string{"a*b", "q?", "[x]", `back\slash`, "!bang", "#hash", "trail ", "sub/.env"}
	var l Layout
	for _, p := range paths {
		l.Mounts = append(l.Mounts, Mount{Path: p, Mask: true})
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	excludes := filepath.Join(t.TempDir(), "excludes")
	write(t, excludes, l.Excludes())
	ignored := func(p string) bool {
		return exec.Command("git", "-C", dir, "-c", "core.excludesFile="+excludes, "check-ignore", "-q", "--no-index", p).Run() == nil
	}
	for _, p := range paths {
		if !ignored(p) {
			t.Errorf("%q not ignored", p)
		}
	}
	for _, p := range []string{"aXb", "qq", "x", "trail", "other/sub/.env", ".env"} {
		if ignored(p) {
			t.Errorf("%q ignored, want only the listed paths", p)
		}
	}
}

func TestIgnored(t *testing.T) {
	ig, err := ParseIgnore([]byte("# editor\n.claude/settings.local.json\n.idea/\n*.log\n!keep.log\n"), "test")
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{
		".claude/settings.local.json": true,
		".claude/settings.json":       false,
		".idea/workspace.xml":         true,
		"app/.idea/x/y.xml":           true,
		"debug.log":                   true,
		"logs/keep.log":               false,
		"main.go":                     false,
	} {
		if got := ig.Ignored(path); got != want {
			t.Errorf("Ignored(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestIgnoredMatchesGit(t *testing.T) {
	isolateGit(t)
	rules := "build/\n/top.txt\nsub/**/x\n*.tmp\n!important.tmp\n"
	ig, err := ParseIgnore([]byte(rules), "test")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	excludes := filepath.Join(t.TempDir(), "excludes")
	write(t, excludes, rules)
	for _, p := range []string{"build/out.bin", "a/build/out.bin", "top.txt", "a/top.txt", "sub/a/b/x", "sub/x", "a.tmp", "important.tmp", "build/important.tmp"} {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, full, "")
		gitSays := exec.Command("git", "-C", dir, "-c", "core.excludesFile="+excludes, "check-ignore", "-q", p).Run() == nil
		if got := ig.Ignored(p); got != gitSays {
			t.Errorf("Ignored(%q) = %v, git says %v", p, got, gitSays)
		}
	}
}

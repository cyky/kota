package mask

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"localllm/internal/sandbox"
)

// isolateGit keeps the user's global and system git config out of the tests.
func isolateGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustPlan(t *testing.T, project string) Layout {
	t.Helper()
	p, err := Plan(project)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var (
	gitPin       = Mount{Path: ".git", Dir: true, Pin: true}
	gitHooks     = Mount{Path: ".git/hooks", Dir: true}
	gitConfigRO  = Mount{Path: ".git/config"}
	gitCommondir = Mount{Path: ".git/commondir", Seed: ".\n"}
	envrcMissing = Mount{Path: ".envrc", Placeholder: true}
)

func TestPlan(t *testing.T) {
	isolateGit(t)
	tests := []struct {
		name  string
		setup func(t *testing.T) string
		want  []Mount
		note  string // substring of a note, if one is expected
	}{
		{
			name:  "not a git repo",
			setup: func(t *testing.T) string { return t.TempDir() },
			want:  []Mount{envrcMissing},
			note:  "not a git repository",
		},
		{
			name:  "git repo",
			setup: repo,
			want:  []Mount{envrcMissing, gitPin, gitCommondir, gitConfigRO, gitHooks},
		},
		{
			name: "existing envrc",
			setup: func(t *testing.T) string {
				dir := repo(t)
				write(t, filepath.Join(dir, ".envrc"), "use flake\n")
				return dir
			},
			want: []Mount{{Path: ".envrc"}, gitPin, gitCommondir, gitConfigRO, gitHooks},
		},
		{
			name: "missing hooks dir",
			setup: func(t *testing.T) string {
				dir := repo(t)
				if err := os.RemoveAll(filepath.Join(dir, ".git/hooks")); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			want: []Mount{envrcMissing, gitPin, gitCommondir, gitConfigRO, {Path: ".git/hooks", Dir: true, Placeholder: true}},
		},
		{
			name: "hooksPath inside project",
			setup: func(t *testing.T) string {
				dir := repo(t)
				write(t, filepath.Join(dir, ".husky/_/pre-commit"), "")
				git(t, dir, "config", "core.hooksPath", ".husky/_")
				return dir
			},
			want: []Mount{envrcMissing, gitPin, gitCommondir, gitConfigRO, gitHooks, {Path: ".husky", Dir: true, Pin: true}, {Path: ".husky/_", Dir: true}},
			note: "core.hooksPath is .husky/_",
		},
		{
			name: "hooksPath missing",
			setup: func(t *testing.T) string {
				dir := repo(t)
				git(t, dir, "config", "core.hooksPath", "githooks")
				return dir
			},
			want: []Mount{envrcMissing, gitPin, gitCommondir, gitConfigRO, gitHooks, {Path: "githooks", Dir: true, Placeholder: true}},
			note: "core.hooksPath is githooks",
		},
		{
			name: "hooksPath with missing parents",
			setup: func(t *testing.T) string {
				dir := repo(t)
				git(t, dir, "config", "core.hooksPath", "tools/git/hooks")
				return dir
			},
			want: []Mount{envrcMissing, gitPin, gitCommondir, gitConfigRO, gitHooks, {Path: "tools", Dir: true, Placeholder: true}},
			note: "core.hooksPath is tools/git/hooks",
		},
		{
			name: "hooksPath absolute inside project",
			setup: func(t *testing.T) string {
				dir := repo(t)
				git(t, dir, "config", "core.hooksPath", filepath.Join(dir, "githooks"))
				return dir
			},
			want: []Mount{envrcMissing, gitPin, gitCommondir, gitConfigRO, gitHooks, {Path: "githooks", Dir: true, Placeholder: true}},
			note: "core.hooksPath is githooks",
		},
		{
			name: "hooksPath from global config",
			setup: func(t *testing.T) string {
				dir := repo(t)
				write(t, os.Getenv("GIT_CONFIG_GLOBAL"), "[core]\n\thooksPath = .githooks\n")
				t.Cleanup(func() { os.Remove(os.Getenv("GIT_CONFIG_GLOBAL")) })
				return dir
			},
			want: []Mount{envrcMissing, gitPin, gitCommondir, gitConfigRO, gitHooks, {Path: ".githooks", Dir: true, Placeholder: true}},
			note: "core.hooksPath is .githooks",
		},
		{
			name: "hooksPath outside project",
			setup: func(t *testing.T) string {
				dir := repo(t)
				git(t, dir, "config", "core.hooksPath", t.TempDir())
				return dir
			},
			want: []Mount{envrcMissing, gitPin, gitCommondir, gitConfigRO, gitHooks},
		},
		{
			name: "hooksPath is the default",
			setup: func(t *testing.T) string {
				dir := repo(t)
				git(t, dir, "config", "core.hooksPath", ".git/hooks")
				return dir
			},
			want: []Mount{envrcMissing, gitPin, gitCommondir, gitConfigRO, gitHooks},
		},
		{
			name: "worktree config enabled",
			setup: func(t *testing.T) string {
				dir := repo(t)
				git(t, dir, "config", "extensions.worktreeConfig", "true")
				return dir
			},
			want: []Mount{envrcMissing, gitPin, gitCommondir, gitConfigRO, {Path: ".git/config.worktree", Placeholder: true}, gitHooks},
		},
		{
			name: "linked worktree",
			setup: func(t *testing.T) string {
				main := repo(t)
				wt := filepath.Join(t.TempDir(), "wt")
				git(t, main, "worktree", "add", "-q", wt)
				return wt
			},
			want: []Mount{envrcMissing, {Path: ".git"}},
			note: "git does not work inside the sandbox",
		},
		{
			name: "submodule",
			setup: func(t *testing.T) string {
				sub := repo(t)
				dir := repo(t)
				git(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "libs/sub")
				return dir
			},
			want: []Mount{
				envrcMissing, gitPin, gitCommondir, gitConfigRO, gitHooks,
				{Path: ".git/modules", Dir: true, Pin: true},
				{Path: ".git/modules/libs", Dir: true, Pin: true},
				{Path: ".git/modules/libs/sub", Dir: true, Pin: true},
				{Path: ".git/modules/libs/sub/commondir", Seed: ".\n"},
				{Path: ".git/modules/libs/sub/config"},
				{Path: ".git/modules/libs/sub/hooks", Dir: true},
				{Path: "libs", Dir: true, Pin: true},
				{Path: "libs/sub", Dir: true, Pin: true},
				{Path: "libs/sub/.git"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := mustPlan(t, tt.setup(t))
			if !slices.Equal(p.Mounts, tt.want) {
				t.Errorf("mounts:\n got %+v\nwant %+v", p.Mounts, tt.want)
			}
			hasNote := slices.ContainsFunc(p.Notes, func(n string) bool { return tt.note != "" && strings.Contains(n, tt.note) })
			if tt.note != "" && !hasNote {
				t.Errorf("notes = %q, want one containing %q", p.Notes, tt.note)
			}
			if tt.note == "" && len(p.Notes) > 0 {
				t.Errorf("unexpected notes %q", p.Notes)
			}
		})
	}
}

func TestPlanRefusesSymlinks(t *testing.T) {
	isolateGit(t)
	for _, rel := range []string{".envrc", ".git/hooks", ".git/config"} {
		t.Run(rel, func(t *testing.T) {
			dir := repo(t)
			path := filepath.Join(dir, rel)
			target := filepath.Join(t.TempDir(), "elsewhere")
			if err := os.Rename(path, target); err != nil {
				write(t, target, "")
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			if _, err := Plan(dir); err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Errorf("err = %v, want a symlink refusal", err)
			}
		})
	}
}

func TestCleanup(t *testing.T) {
	dir := t.TempDir()
	p := Layout{Mounts: []Mount{
		{Path: ".envrc", Placeholder: true},
		{Path: "githooks", Dir: true, Placeholder: true},
		{Path: "kept", Placeholder: true},
		{Path: "kept-dir", Dir: true, Placeholder: true},
		{Path: "real"},
		{Path: "missing", Placeholder: true},
	}}
	write(t, filepath.Join(dir, ".envrc"), "")
	write(t, filepath.Join(dir, "kept"), "written on the host meanwhile")
	write(t, filepath.Join(dir, "kept-dir/file"), "")
	write(t, filepath.Join(dir, "real"), "")
	if err := os.Mkdir(filepath.Join(dir, "githooks"), 0o755); err != nil {
		t.Fatal(err)
	}

	p.Cleanup(dir)

	for _, gone := range []string{".envrc", "githooks"} {
		if _, err := os.Lstat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s still exists", gone)
		}
	}
	for _, kept := range []string{"kept", "kept-dir/file", "real"} {
		if _, err := os.Lstat(filepath.Join(dir, kept)); err != nil {
			t.Errorf("%s removed: %v", kept, err)
		}
	}
}

func TestBind(t *testing.T) {
	scratch := t.TempDir()
	l := Layout{Mounts: []Mount{
		{Path: ".envrc", Placeholder: true},
		{Path: ".git", Dir: true, Pin: true},
		{Path: ".git/config"},
		{Path: "githooks", Dir: true, Placeholder: true},
	}}
	pinned, readOnly, err := l.Bind("/p", scratch)
	if err != nil {
		t.Fatal(err)
	}
	wantPinned := []sandbox.Mount{{Source: "/p/.git", Target: "/workspace/.git"}}
	wantRO := []sandbox.Mount{
		{Source: filepath.Join(scratch, "empty-file"), Target: "/workspace/.envrc"},
		{Source: "/p/.git/config", Target: "/workspace/.git/config"},
		{Source: filepath.Join(scratch, "empty-dir"), Target: "/workspace/githooks"},
	}
	if !slices.Equal(pinned, wantPinned) || !slices.Equal(readOnly, wantRO) {
		t.Errorf("pinned = %+v\nread-only = %+v", pinned, readOnly)
	}
	if info, err := os.Stat(filepath.Join(scratch, "empty-file")); err != nil || info.Size() != 0 {
		t.Errorf("empty file: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(scratch, "empty-dir")); err != nil || len(entries) != 0 {
		t.Errorf("empty dir: %v %v", entries, err)
	}
}

func TestPlanProtectsLinkedWorktrees(t *testing.T) {
	isolateGit(t)
	dir := repo(t)
	git(t, dir, "config", "extensions.worktreeConfig", "true")
	git(t, dir, "worktree", "add", "-q", filepath.Join(t.TempDir(), "wt"))
	p := mustPlan(t, dir)
	for _, want := range []Mount{
		{Path: ".git/worktrees", Dir: true, Pin: true},
		{Path: ".git/worktrees/wt", Dir: true, Pin: true},
		{Path: ".git/worktrees/wt/commondir"},
		{Path: ".git/worktrees/wt/config.worktree", Placeholder: true},
	} {
		if !slices.Contains(p.Mounts, want) {
			t.Errorf("missing %+v in %+v", want, p.Mounts)
		}
	}
}

func TestPlanRefusesSymlinkOnTheWay(t *testing.T) {
	isolateGit(t)
	dir := repo(t)
	// core.hooksPath = tools/hooks, where tools is a link the agent could
	// replace with a real directory holding its own hooks.
	real := filepath.Join(dir, "real-tools")
	write(t, filepath.Join(real, "hooks/pre-commit"), "")
	if err := os.Symlink(real, filepath.Join(dir, "tools")); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "config", "core.hooksPath", "tools/hooks")
	if _, err := Plan(dir); err == nil || !strings.Contains(err.Error(), "tools is a symlink") {
		t.Errorf("err = %v, want refusal naming tools", err)
	}
}

func TestPrepareAndCleanupSeeds(t *testing.T) {
	dir := t.TempDir()
	l := Layout{Mounts: []Mount{{Path: "seeded", Seed: ".\n"}, {Path: "changed", Seed: ".\n"}}}
	if err := l.Prepare(dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "seeded")); string(b) != ".\n" {
		t.Errorf("seeded = %q", b)
	}
	write(t, filepath.Join(dir, "changed"), "../elsewhere\n")
	l.Cleanup(dir)
	if _, err := os.Lstat(filepath.Join(dir, "seeded")); !os.IsNotExist(err) {
		t.Error("unchanged seed not removed")
	}
	if _, err := os.Lstat(filepath.Join(dir, "changed")); err != nil {
		t.Error("changed seed removed")
	}
	if err := l.Prepare(dir); err == nil {
		t.Error("Prepare overwrote an existing file")
	}
}

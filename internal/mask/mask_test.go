package mask

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"localllm/internal/deps"
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

// mustPlan plans project without default mask rules, so tests of the git
// protections only see those.
func mustPlan(t *testing.T, project string) Layout {
	t.Helper()
	p, err := Plan(project, nil)
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
			// Every project gets the rules file and the project config
			// protected.
			want := append(slices.Clone(tt.want), Mount{Path: IgnoreFile, Placeholder: true}, Mount{Path: deps.ConfigFile, Placeholder: true})
			slices.SortStableFunc(want, func(a, b Mount) int { return strings.Compare(a.Path, b.Path) })
			if !slices.Equal(p.Mounts, want) {
				t.Errorf("mounts:\n got %+v\nwant %+v", p.Mounts, want)
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
			if _, err := Plan(dir, nil); err == nil || !strings.Contains(err.Error(), "symlink") {
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
	if _, err := Plan(dir, nil); err == nil || !strings.Contains(err.Error(), "tools is a symlink") {
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

// masked returns the masked paths of a layout, with "/" after directories.
func masked(l Layout) []string {
	var out []string
	for _, m := range l.Mounts {
		if m.Mask {
			p := m.Path
			if m.Dir {
				p += "/"
			}
			out = append(out, p)
		}
	}
	return out
}

func TestPlanMasks(t *testing.T) {
	isolateGit(t)
	tests := []struct {
		name   string
		files  []string // created empty; a trailing "/" makes a directory
		ignore string   // .localllmignore content, if any
		want   []string
	}{
		{
			name:  "defaults",
			files: []string{".env", ".env.example", "api/.env.production", "certs/tls.key", "id_ed25519", "id_ed25519.pub", "main.go", "deploy/.aws/credentials"},
			want:  []string{".env", "api/.env.production", "certs/tls.key", "deploy/.aws/", "id_ed25519"},
		},
		{
			name:   "ignore file patterns",
			files:  []string{"config/prod.yml", "app/config/prod.yml", "secrets/a.txt", "secrets/b/c.txt", "notes/creds.json", "creds.json"},
			ignore: "# project secrets\n/config/prod.yml\nsecrets/\n**/creds.json\n",
			want:   []string{"config/prod.yml", "creds.json", "notes/creds.json", "secrets/"},
		},
		{
			name:   "negation",
			files:  []string{"keys/a.pem", "keys/public.pem", ".env"},
			ignore: "!public.pem\n!.env\n",
			want:   []string{"keys/a.pem"},
		},
		{
			name:   "nothing inside a masked dir is re-included",
			files:  []string{"vault/keep.txt", "vault/x.txt"},
			ignore: "vault/\n!vault/keep.txt\n",
			want:   []string{"vault/"},
		},
		{
			name:  "heavy dirs are skipped",
			files: []string{"node_modules/pkg/.env", "build/.env", ".venv/lib/id_rsa", "src/.env"},
			want:  []string{"src/.env"},
		},
		{
			name:   "envrc can be masked",
			files:  []string{".envrc"},
			ignore: ".envrc\n",
			want:   []string{".envrc"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := repo(t)
			for _, f := range tt.files {
				if strings.HasSuffix(f, "/") {
					if err := os.MkdirAll(filepath.Join(dir, f), 0o755); err != nil {
						t.Fatal(err)
					}
					continue
				}
				write(t, filepath.Join(dir, f), "secret")
			}
			if tt.ignore != "" {
				write(t, filepath.Join(dir, IgnoreFile), tt.ignore)
			}
			l, err := Plan(dir, DefaultRules)
			if err != nil {
				t.Fatal(err)
			}
			if got := masked(l); !slices.Equal(got, tt.want) {
				t.Errorf("masked = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPlanMaskedEnvrcIsReadOnlyAndEmpty(t *testing.T) {
	isolateGit(t)
	dir := repo(t)
	write(t, filepath.Join(dir, ".envrc"), "export TOKEN=x\n")
	write(t, filepath.Join(dir, IgnoreFile), ".envrc\n")
	l, err := Plan(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(l.Mounts, Mount{Path: ".envrc", Mask: true}) {
		t.Errorf("mounts = %+v", l.Mounts)
	}
}

func TestPlanMaskedDirDropsMountsInside(t *testing.T) {
	isolateGit(t)
	dir := repo(t)
	write(t, filepath.Join(dir, ".husky/_/pre-commit"), "")
	git(t, dir, "config", "core.hooksPath", ".husky/_")
	write(t, filepath.Join(dir, IgnoreFile), ".husky/\n")
	l, err := Plan(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range l.Mounts {
		if strings.HasPrefix(m.Path, ".husky") && m != (Mount{Path: ".husky", Dir: true, Mask: true}) {
			t.Errorf("unexpected mount %+v next to the .husky mask", m)
		}
	}
	if !slices.Contains(masked(l), ".husky/") {
		t.Errorf("masked = %q", masked(l))
	}
}

func TestPlanMaskSymlinks(t *testing.T) {
	isolateGit(t)
	dir := repo(t)
	write(t, filepath.Join(dir, "config/real.env"), "secret")
	outside := filepath.Join(t.TempDir(), "outside.env")
	write(t, outside, "secret")
	for link, target := range map[string]string{
		".env":          "config/real.env", // inside: hide the target
		"other/.env":    outside,           // outside: not reachable anyway
		"dangling/.env": "missing",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, link)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	l, err := Plan(dir, DefaultRules)
	if err != nil {
		t.Fatal(err)
	}
	if got := masked(l); !slices.Equal(got, []string{"config/real.env"}) {
		t.Errorf("masked = %q", got)
	}
}

func TestPlanWarnsAboutTrackedMasks(t *testing.T) {
	isolateGit(t)
	dir := repo(t)
	write(t, filepath.Join(dir, ".env.production"), "tracked")
	write(t, filepath.Join(dir, "deploy/.aws/config"), "tracked")
	write(t, filepath.Join(dir, ".env"), "untracked")
	git(t, dir, "add", "-f", ".env.production", "deploy/.aws/config")
	git(t, dir, "commit", "-q", "-m", "oops")

	l, err := Plan(dir, DefaultRules)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(l.Warnings, "\n")
	for _, want := range []string{"masked .env.production is tracked", "masked deploy/.aws is tracked"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q lack %q", l.Warnings, want)
		}
	}
	if strings.Contains(joined, "masked .env is") {
		t.Errorf("untracked .env warned about: %q", l.Warnings)
	}
}

func TestPlanSkipsLargeNodeModulesQuickly(t *testing.T) {
	isolateGit(t)
	dir := repo(t)
	nm := filepath.Join(dir, "node_modules")
	for i := range 200 {
		pkg := filepath.Join(nm, "pkg"+strconv.Itoa(i), "lib")
		if err := os.MkdirAll(pkg, 0o755); err != nil {
			t.Fatal(err)
		}
		for j := range 50 {
			if err := os.WriteFile(filepath.Join(pkg, strconv.Itoa(j)+".js"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	start := time.Now()
	if _, err := Plan(dir, DefaultRules); err != nil {
		t.Fatal(err)
	}
	// Walking 10k files takes far longer than planning a near-empty repo.
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("Plan took %s with node_modules present", d)
	}
}

func TestPlanRefusesBadIgnoreFile(t *testing.T) {
	isolateGit(t)
	dir := repo(t)
	if err := os.Symlink(filepath.Join(t.TempDir(), "rules"), filepath.Join(dir, IgnoreFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := Plan(dir, DefaultRules); err == nil {
		t.Error("symlinked .localllmignore accepted")
	}
}

func TestPlanMaskKeepsPinsAboveIt(t *testing.T) {
	isolateGit(t)
	dir := repo(t)
	write(t, filepath.Join(dir, "tools/git/hooks/pre-commit"), "")
	git(t, dir, "config", "core.hooksPath", "tools/git/hooks")
	write(t, filepath.Join(dir, IgnoreFile), "tools/git/\n")
	l, err := Plan(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Without the pin on tools, the agent could rename it and recreate
	// tools/git/hooks with its own hooks.
	for _, want := range []Mount{{Path: "tools", Dir: true, Pin: true}, {Path: "tools/git", Dir: true, Mask: true}} {
		if !slices.Contains(l.Mounts, want) {
			t.Errorf("missing %+v in %+v", want, l.Mounts)
		}
	}
	for _, m := range l.Mounts {
		if strings.HasPrefix(m.Path, "tools/git/") {
			t.Errorf("mount inside the mask: %+v", m)
		}
	}
}

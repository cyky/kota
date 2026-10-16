//go:build integration

package nohistory

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"kota/internal/docker"
)

const testImage = "kota-base:latest"

// cacheDir creates a directory under the user cache dir, which Docker
// Desktop-style VMs share by default (unlike macOS's per-user temp dir).
func cacheDir(t *testing.T) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(cache, "kota-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func dockerRunner(mounts []string, dir string, cmd ...string) (string, error) {
	return docker.RunOffline(testImage, os.Getuid(), os.Getgid(), mounts, dir, cmd...)
}

func dockerWritable(mounts []string, writable, dir string, cmd ...string) (string, error) {
	return docker.RunOfflineWritable(testImage, os.Getuid(), os.Getgid(), mounts, writable, dir, cmd...)
}

func needDocker(t *testing.T) {
	t.Helper()
	if exec.Command("docker", "image", "inspect", testImage).Run() != nil {
		t.Skip("docker or " + testImage + " not available; run 'kota build'")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// git runs host git in a repository the test made; the code under test
// never does.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCloneLeavesHistoryBehind(t *testing.T) {
	needDocker(t)
	project := filepath.Join(cacheDir(t), "app")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, project, "init", "-q")
	os.WriteFile(filepath.Join(project, ".env"), []byte("TOKEN=hunter2\n"), 0o644)
	os.WriteFile(filepath.Join(project, "README"), []byte("v1\n"), 0o644)
	git(t, project, "add", ".")
	git(t, project, "commit", "-q", "-m", "add secret")
	secret := git(t, project, "rev-parse", "HEAD:.env")
	git(t, project, "rm", "-q", "--cached", ".env")
	os.Remove(filepath.Join(project, ".env"))
	os.WriteFile(filepath.Join(project, "README"), []byte("v2\n"), 0o644)
	git(t, project, "commit", "-q", "-am", "remove secret")
	head := git(t, project, "rev-parse", "HEAD")
	git(t, project, "remote", "add", "origin", "https://example.com/secret-remote.git")
	git(t, project, "config", "kota.marker", "original-config")
	os.WriteFile(filepath.Join(project, ".git", "hooks", "post-checkout"), []byte("#!/bin/sh\ntouch \"$GIT_DIR/../hooked\"\n"), 0o755)

	copies := cacheDir(t)
	dest := filepath.Join(copies, "app-copy")
	if err := Clone(project, dest, dockerWritable); err != nil {
		t.Fatal(err)
	}

	if got := git(t, dest, "rev-list", "--all", "--count"); got != "1" {
		t.Errorf("copy has %s commits, want 1", got)
	}
	if got := git(t, dest, "rev-parse", "HEAD"); got != head {
		t.Errorf("copy HEAD = %s, want %s", got, head)
	}
	if got := git(t, dest, "remote"); got != "" {
		t.Errorf("copy remotes = %q", got)
	}
	if exec.Command("git", "-C", dest, "cat-file", "-e", secret).Run() == nil {
		t.Error("the removed secret's blob is in the copy")
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "README")); string(got) != "v2\n" {
		t.Errorf("README = %q", got)
	}
	for _, dir := range []string{dest, project} {
		if _, err := os.Stat(filepath.Join(dir, "hooked")); err == nil {
			t.Errorf("the original's hook ran (%s)", dir)
		}
	}
	// Nothing in the copy's git dir names the original, its remote or its
	// config.
	err := filepath.WalkDir(filepath.Join(dest, ".git"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		for _, leak := range []string{project, "secret-remote", "original-config", "post-checkout"} {
			if strings.Contains(string(b), leak) {
				t.Errorf("%s names %q", path, leak)
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, ".git", "info", "exclude")); !strings.Contains(string(b), "/kota.yaml\n") {
		t.Errorf("exclude = %q", b)
	}

	// The copy is a working repository: a commit there builds on HEAD.
	os.WriteFile(filepath.Join(dest, "README"), []byte("v3\n"), 0o644)
	git(t, dest, "commit", "-q", "-am", "agent work")
	if got := git(t, dest, "rev-parse", "HEAD~1"); got != head {
		t.Errorf("commit parent = %s, want %s", got, head)
	}
}

func TestInspectRefusesUnbornHead(t *testing.T) {
	needDocker(t)
	project := cacheDir(t)
	git(t, project, "init", "-q")
	if _, err := Inspect(project, dockerRunner); err == nil || !strings.Contains(err.Error(), "no commits") {
		t.Errorf("unborn HEAD: %v", err)
	}
}

func TestInspectFindsWhatTheCopyLacks(t *testing.T) {
	needDocker(t)
	project := filepath.Join(cacheDir(t), "app")
	sub := filepath.Join(cacheDir(t), "sub")
	for _, dir := range []string{project, sub} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		git(t, dir, "init", "-q")
	}
	os.WriteFile(filepath.Join(sub, "f"), []byte("x\n"), 0o644)
	git(t, sub, "add", ".")
	git(t, sub, "commit", "-q", "-m", "sub")

	os.WriteFile(filepath.Join(project, ".gitattributes"), []byte("*.bin filter=lfs diff=lfs merge=lfs -text\n"), 0o644)
	os.WriteFile(filepath.Join(project, "big.bin"), []byte("version https://git-lfs.github.com/spec/v1\n"), 0o644)
	os.WriteFile(filepath.Join(project, "README"), []byte("v1\n"), 0o644)
	git(t, project, "add", ".")
	git(t, project, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "lib/sub")
	git(t, project, "commit", "-q", "-m", "init")
	os.WriteFile(filepath.Join(project, "README"), []byte("v2\n"), 0o644)
	os.WriteFile(filepath.Join(project, "kota.yaml"), []byte("engine: {}\n"), 0o644)

	m, err := Inspect(project, dockerRunner)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(m.Submodules, []string{"lib/sub"}) || !slices.Equal(m.LFS, []string{"big.bin"}) ||
		!slices.Equal(m.Changes, []string{" M README"}) {
		t.Errorf("missing = %+v", m)
	}
}

// settle waits out Docker's file sharing cache. A file host git replaced
// (by renaming a new one over it) shortly after a container read it can
// read as missing or broken in the next container for a moment.
func settle() { time.Sleep(2 * time.Second) }

// returnSetup makes a project with one commit, its copy, and a bundle dir.
func returnSetup(t *testing.T) (project, dest, base, bundle string) {
	t.Helper()
	project = filepath.Join(cacheDir(t), "app")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, project, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(project, "README"), []byte("v1\n"), 0o644)
	git(t, project, "add", ".")
	git(t, project, "commit", "-q", "-m", "start")
	base = git(t, project, "rev-parse", "HEAD")
	dest = filepath.Join(cacheDir(t), "copy")
	if err := Clone(project, dest, dockerWritable); err != nil {
		t.Fatal(err)
	}
	out := cacheDir(t)
	return project, dest, base, filepath.Join(out, "return.bundle")
}

func TestReturnBringsHeadBack(t *testing.T) {
	needDocker(t)
	project, dest, base, bundle := returnSetup(t)
	// Markers in the two writable mounts: the bundle's directory during the
	// export, the original's git dir during the import.
	markers := []string{filepath.Join(filepath.Dir(bundle), "ran"), filepath.Join(project, ".git", "ran")}
	touch := "touch " + markers[0] + " " + markers[1] + " 2>/dev/null; true"
	// The agent's work, plus config and hooks it planted in the copy.
	os.WriteFile(filepath.Join(dest, "README"), []byte("v2\n"), 0o644)
	git(t, dest, "commit", "-q", "-am", "agent one")
	os.WriteFile(filepath.Join(dest, "NEW"), []byte("x\n"), 0o644)
	git(t, dest, "add", "NEW")
	git(t, dest, "commit", "-q", "-m", "agent two")
	head := git(t, dest, "rev-parse", "HEAD")
	hook := "#!/bin/sh\n" + touch + "\n"
	os.MkdirAll(filepath.Join(dest, ".git", "hooks"), 0o755)
	for _, name := range []string{"reference-transaction", "pre-push", "post-checkout"} {
		os.WriteFile(filepath.Join(dest, ".git", "hooks", name), []byte(hook), 0o755)
	}
	git(t, dest, "config", "core.fsmonitor", touch)
	git(t, dest, "config", "uploadpack.packObjectsHook", touch+";")
	// A hook in the original runs on ref updates; the import turns hooks off.
	os.WriteFile(filepath.Join(project, ".git", "hooks", "reference-transaction"), []byte(hook), 0o755)
	userBranch := git(t, project, "symbolic-ref", "HEAD")

	settle()
	if err := Export(dest, base, bundle, dockerWritable); err != nil {
		t.Fatal(err)
	}
	branch := BranchName("pi", time.Date(2026, 10, 16, 14, 32, 5, 0, time.Local))
	got, err := Import(project, bundle, base, branch, dockerWritable)
	if err != nil {
		t.Fatal(err)
	}
	if got.Commit != head || got.Commits != 2 || got.Branch != branch || git(t, project, "rev-parse", branch) != head {
		t.Errorf("returned %+v, branch at %s; want %s with 2 commits", got, git(t, project, "rev-parse", branch), head)
	}
	if n := git(t, project, "rev-list", "--count", base+".."+branch); n != "2" {
		t.Errorf("branch has %s new commits, want 2", n)
	}
	if refs := git(t, project, "for-each-ref", "--format=%(refname)", "refs/kota-incoming"); refs != "" {
		t.Errorf("incoming ref left: %s", refs)
	}
	if git(t, project, "symbolic-ref", "HEAD") != userBranch || git(t, project, "rev-parse", "HEAD") != base {
		t.Error("the original's current branch changed")
	}
	if b, _ := os.ReadFile(filepath.Join(project, "README")); string(b) != "v1\n" {
		t.Errorf("the original's working tree changed: README = %q", b)
	}
	for _, m := range markers {
		if _, err := os.Stat(m); err == nil {
			t.Errorf("a planted hook or config command ran (%s)", m)
		}
	}

	// The same name again: the branch isn't replaced.
	if _, err := Import(project, bundle, base, branch, dockerWritable); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second import: %v", err)
	}

	// Control: host git runs the original's hook on a ref update, so the
	// hook above is a real one.
	git(t, project, "update-ref", "refs/heads/control", "HEAD")
	if _, err := os.Stat(markers[1]); err != nil {
		t.Errorf("control: the planted hook didn't run under host git: %v", err)
	}
}

func TestReturnRefusesRewrittenHistory(t *testing.T) {
	needDocker(t)
	project, dest, base, bundle := returnSetup(t)
	// The agent replaced the history with a new root.
	git(t, dest, "checkout", "-q", "--orphan", "fresh")
	git(t, dest, "commit", "-q", "-m", "new root")
	settle()
	if err := Export(dest, base, bundle, dockerWritable); err != nil {
		t.Fatal(err)
	}
	branch := BranchName("pi", time.Now())
	if _, err := Import(project, bundle, base, branch, dockerWritable); err != ErrRewritten {
		t.Errorf("import = %v, want ErrRewritten", err)
	}
	if refs := git(t, project, "for-each-ref", "--format=%(refname)", "refs/kota-incoming", "refs/heads/kota"); refs != "" {
		t.Errorf("refs left: %s", refs)
	}
}

func TestUncommittedInCopy(t *testing.T) {
	needDocker(t)
	_, dest, _, _ := returnSetup(t)
	excludes := filepath.Join(cacheDir(t), "git-excludes")
	os.WriteFile(excludes, []byte("/.envrc\n"), 0o444)
	os.WriteFile(filepath.Join(dest, ".envrc"), nil, 0o644) // a placeholder
	os.WriteFile(filepath.Join(dest, "kota.yaml"), []byte("{}\n"), 0o644)
	os.WriteFile(filepath.Join(dest, "README"), []byte("changed\n"), 0o644)
	got, err := Uncommitted(dest, excludes, dockerRunner)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{" M README"}; !slices.Equal(got, want) {
		t.Errorf("uncommitted = %q, want %q", got, want)
	}
}

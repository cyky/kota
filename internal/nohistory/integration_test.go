//go:build integration

package nohistory

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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

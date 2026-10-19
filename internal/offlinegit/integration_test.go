//go:build integration

package offlinegit

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"kota/internal/pathpolicy"
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

// dockerRunner is the production adapter, with a policy whose allowed root
// is the user cache dir the tests' repositories live in.
func dockerRunner(t *testing.T) Runner {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	return Docker(testImage, os.Getuid(), os.Getgid(), pathpolicy.Default(t.TempDir(), cache))
}

// A repository the agent created can name a clean filter, which git status
// runs. In the container it runs offline and can't write.
func TestReadStatusRunsFiltersOffline(t *testing.T) {
	if exec.Command("docker", "image", "inspect", testImage).Run() != nil {
		t.Skip("docker or " + testImage + " not available; run 'kota build'")
	}
	isolateGit(t)
	project := cacheDir(t)
	outside := filepath.Join(cacheDir(t), "marker")
	inside := filepath.Join(project, "marker")

	git(t, project, "init", "-q")
	os.WriteFile(filepath.Join(project, "f"), []byte("hi\n"), 0o644)
	git(t, project, "add", "f")
	git(t, project, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "init")
	git(t, project, "config", "filter.x.clean", "touch "+outside+"; touch "+inside+"; cat")
	os.WriteFile(filepath.Join(project, ".gitattributes"), []byte("* filter=x\n"), 0o644)
	// A changed timestamp makes git status hash the file through the filter.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(project, "f"), old, old); err != nil {
		t.Fatal(err)
	}

	s, err := ReadStatus(project, dockerRunner(t))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"?? .gitattributes"}; !s.Git || !slices.Equal(s.Changes, want) {
		t.Errorf("status = %+v, want changes %q", s, want)
	}
	for _, m := range []string{outside, inside} {
		if _, err := os.Stat(m); err == nil {
			t.Errorf("filter wrote %s on the host", m)
		}
	}

	// Control: host git runs the filter, so the setup above is a real attack.
	if _, err := ReadStatus(project, hostRunner(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("control: host git status did not run the filter: %v", err)
	}
}

// A linked worktree's git dir lies outside the project; its status still
// works with the mounts FindRepo returns. The comma checks the quoting of
// docker's --mount value.
func TestReadStatusLinkedWorktreeInContainer(t *testing.T) {
	if exec.Command("docker", "image", "inspect", testImage).Run() != nil {
		t.Skip("docker or " + testImage + " not available; run 'kota build'")
	}
	isolateGit(t)
	root := cacheDir(t)
	main := filepath.Join(root, "ma,in")
	os.Mkdir(main, 0o755)
	git(t, main, "init", "-q")
	git(t, main, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(root, "wt")
	git(t, main, "worktree", "add", "-q", wt)
	os.WriteFile(filepath.Join(wt, "new.txt"), []byte("x"), 0o644)

	s, err := ReadStatus(wt, dockerRunner(t))
	if err != nil || !s.Git || !slices.Equal(s.Changes, []string{"?? new.txt"}) {
		t.Errorf("worktree status = %+v, %v", s, err)
	}
}

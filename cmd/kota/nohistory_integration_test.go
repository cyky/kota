//go:build integration

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"kota/internal/docker"
	"kota/internal/hostdata"
	"kota/internal/sandbox"
)

// TestNoHistorySession runs a shell session with --no-history on a project
// whose history holds a secret, and checks what the session can see and
// that the copy is kept with the agent's commit, apart from the original.
func TestNoHistorySession(t *testing.T) {
	requireBaseImage(t)
	t.Setenv("KOTA_GATEWAY_KEY", "")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(config, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(cache, "kota-test-")
	if err != nil {
		t.Fatal(err)
	}
	root, _ = filepath.EvalSymlinks(root)
	t.Cleanup(func() { os.RemoveAll(root) })
	t.Setenv("KOTA_ALLOWED_ROOT", root)
	project := filepath.Join(root, "app")
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(project, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "v1\n")
	write("config/old-secret.yml", "password: HISTORY-SECRET\n")
	write("config/token.txt", "TRACKED-TOKEN\n")
	hostGit(t, project, "init", "-q", "-b", "main")
	hostGit(t, project, "config", "user.name", "t")
	hostGit(t, project, "config", "user.email", "t@example.com")
	hostGit(t, project, "add", ".")
	hostGit(t, project, "commit", "-q", "-m", "start")
	hostGit(t, project, "rm", "-q", "config/old-secret.yml")
	write("README.md", "v2\n")
	hostGit(t, project, "commit", "-q", "-am", "drop the secret")
	hostGit(t, project, "remote", "add", "origin", "https://example.com/app.git")
	head := hostGit(t, project, "rev-parse", "HEAD")
	// Untracked, so not in the commit: it must still mask the tracked token
	// in the copy.
	write(".kotaignore", "config/token.txt\n")

	dirs, err := hostdata.Default()
	if err != nil {
		t.Fatal(err)
	}
	before, err := dirs.CopyDirs(project)
	if err != nil {
		t.Fatal(err)
	}
	sa := sessionArgs{agent: defaultAgent, gatewayURL: stubModelGateway(t), dir: project, shell: true, noHistory: true}
	var copy string
	var stateVolumes []string
	var inside string
	withSession(t, sa, func(*sandbox.Compose) {}, func(sess *session, ds *docker.Session) {
		copy = sess.project
		t.Cleanup(func() { os.RemoveAll(copy) })
		for _, v := range sess.spec.Volumes {
			if strings.HasPrefix(v.Name, "kota-state-") {
				stateVolumes = append(stateVolumes, v.Name)
			}
		}
		inside, err = ds.Capture("bash", "-c", `exec 2>&1
cd /workspace
echo "COMMITS=$(git rev-list --all --count)"
echo "REMOTES=$(git remote | wc -l)"
git log -p --all | grep -v TRACKED-TOKEN
echo "TOKEN=$(cat config/token.txt)"
cat .kotaignore
echo v3 > README.md
git commit -q -m agent README.md && echo COMMITTED
echo DONE`)
		if err != nil {
			t.Fatalf("%v\n%s", err, inside)
		}
	})
	t.Cleanup(func() {
		if available, err := docker.ListVolumes(); err == nil {
			docker.RemoveVolumes(sandbox.ProjectVolumes(project, available)...)
		}
	})
	t.Logf("inside:\n%s", inside)

	if !strings.Contains(inside, "DONE") || !strings.Contains(inside, "COMMITTED") {
		t.Fatalf("script failed:\n%s", inside)
	}
	// The tracked token is in HEAD, which git shows (kota warns about it),
	// but the file reads as empty.
	for _, want := range []string{"COMMITS=1\n", "REMOTES=0\n", "TOKEN=\n", "config/token.txt\n"} {
		if !strings.Contains(inside, want) {
			t.Errorf("inside lacks %q", want)
		}
	}
	for _, leak := range []string{"HISTORY-SECRET", "TRACKED-TOKEN", "example.com/app.git", "password"} {
		if strings.Contains(inside, leak) {
			t.Errorf("the session saw %q", leak)
		}
	}

	after, err := dirs.CopyDirs(project)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(after, copy) || slices.Contains(before, copy) {
		t.Errorf("copy %s not kept as a new copy: %q", copy, after)
	}
	if want := []string{sandbox.StateVolume(project, defaultAgent+sandbox.NoHistoryState)}; !slices.Equal(stateVolumes, want) {
		t.Errorf("state volumes = %q, want %q", stateVolumes, want)
	}
	if got := hostGit(t, copy, "rev-parse", "HEAD~1"); got != head {
		t.Errorf("the agent's commit builds on %s, want %s", got, head)
	}
	if got := hostGit(t, project, "rev-parse", "HEAD"); got != head {
		t.Errorf("the original's HEAD moved to %s", got)
	}
	if got, _ := os.ReadFile(filepath.Join(project, "README.md")); string(got) != "v2\n" {
		t.Errorf("the original's README = %q", got)
	}
	// Placeholders and seeded files were cleaned up in the copy, and the
	// project's untracked .kotaignore isn't staged there.
	if status := hostGit(t, copy, "status", "--porcelain"); status != "" {
		t.Errorf("copy status after the session:\n%s", status)
	}
}

//go:build integration && darwin

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"kota/internal/docker"
	"kota/internal/mask"
	"kota/internal/sandbox"
)

// protectedProject creates a git project with an .envrc and a masked .env
// in a throwaway dir under the user's cache dir, which Docker shares.
func protectedProject(t *testing.T) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(cache, "kota-test-")
	if err != nil {
		t.Fatal(err)
	}
	root, _ = filepath.EvalSymlinks(root)
	t.Cleanup(func() {
		// A failed test may leave locks; clear them so the dir goes.
		filepath.WalkDir(root, func(path string, _ fs.DirEntry, _ error) error {
			if uchg(path) {
				syscall.Chflags(path, 0)
			}
			return nil
		})
		os.RemoveAll(root)
	})
	t.Setenv("KOTA_ALLOWED_ROOT", root)
	project := filepath.Join(root, "app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	hostGit(t, project, "init", "-q")
	for rel, content := range map[string]string{".envrc": "use flake\n", ".env": "KEY=secret\n"} {
		if err := os.WriteFile(filepath.Join(project, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return project
}

// uchg reports whether path has the user-immutable flag.
func uchg(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Flags&0x2 != 0
}

// replace writes content to a new file and renames it over rel, as host
// git and many editors save.
func replace(project, rel, content string) error {
	tmp := filepath.Join(project, rel+".kota-test")
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	defer os.Remove(tmp)
	return os.Rename(tmp, filepath.Join(project, rel))
}

// inPlace is a script that waits past the Docker VM's cache window, then
// prints whether each protection is still mounted, and whether .envrc can
// be written and .env read.
const inPlace = `exec 2>&1
sleep 3
for p in /workspace/.envrc /workspace/.git/commondir /workspace/.env; do
  grep -q " $p " /proc/self/mountinfo && echo "mounted $p" || echo "GONE $p"
done
(: >> .envrc) 2>/dev/null && echo WRITABLE
cat .env`

func checkInPlace(t *testing.T, out string) {
	t.Helper()
	for _, p := range []string{"/workspace/.envrc", "/workspace/.git/commondir", "/workspace/.env"} {
		if !strings.Contains(out, "mounted "+p) {
			t.Errorf("%s not mounted 3s in:\n%s", p, out)
		}
	}
	if strings.Contains(out, "WRITABLE") || strings.Contains(out, "secret") {
		t.Errorf(".envrc writable or .env readable 3s in:\n%s", out)
	}
}

// checkAfter checks nothing in project is left locked, and the
// placeholders and seeds are gone.
func checkAfter(t *testing.T, project string) {
	t.Helper()
	filepath.WalkDir(project, func(path string, _ fs.DirEntry, _ error) error {
		if uchg(path) {
			t.Errorf("%s still locked after the session", path)
		}
		return nil
	})
	for _, rel := range []string{".git/commondir", ".kotaignore", "kota.yaml", "kota.Dockerfile"} {
		if _, err := os.Lstat(filepath.Join(project, rel)); !os.IsNotExist(err) {
			t.Errorf("%s left after the session: %v", rel, err)
		}
	}
}

// Two sessions back to back: the first one's cleanup removes the
// placeholders and the seeded commondir, and the second creates them
// again. Both pass the probes, and the second's mounts still hold 3s in.
func TestBackToBackSessions(t *testing.T) {
	requireBaseImage(t)
	project := protectedProject(t)
	sa := sessionArgs{agent: "opencode", gatewayURL: stubModelGateway(t), dir: project, shell: true}
	withSession(t, sa, func(*sandbox.Compose) {}, func(*session, *docker.Session) {})
	checkAfter(t, project)
	var out string
	withSession(t, sa, func(*sandbox.Compose) {}, func(_ *session, ds *docker.Session) {
		var err error
		if out, err = ds.Capture("bash", "-c", inPlace); err != nil {
			t.Fatal(err)
		}
	})
	checkInPlace(t, out)
	checkAfter(t, project)
}

// During a session, the host can't replace a protected or masked file, so
// their mounts hold, and the end check finds them all.
func TestHostReplaceDuringSession(t *testing.T) {
	requireBaseImage(t)
	project := protectedProject(t)
	sa := sessionArgs{agent: "opencode", gatewayURL: stubModelGateway(t), dir: project, shell: true}
	withSession(t, sa, func(*sandbox.Compose) {}, func(sess *session, ds *docker.Session) {
		for _, rel := range []string{".envrc", ".env"} {
			if err := replace(project, rel, "replaced\n"); err == nil || !strings.Contains(err.Error(), "operation not permitted") {
				t.Errorf("replacing %s on the host = %v, want operation not permitted", rel, err)
			}
		}
		out, err := ds.Capture("bash", "-c", inPlace)
		if err != nil {
			t.Fatal(err)
		}
		checkInPlace(t, out)
		table, err := ds.MountTable()
		if err != nil {
			t.Fatal(err)
		}
		if lost := sess.layout.Lost(table); len(lost) > 0 {
			t.Errorf("mounts gone from the witness: %q", lost)
		}
	})
	checkAfter(t, project)
	if b, _ := os.ReadFile(filepath.Join(project, ".envrc")); string(b) != "use flake\n" {
		t.Errorf(".envrc = %q", b)
	}
}

// Right after a host rename over a file the Docker VM has cached, the VM
// still sees the old file; the pre-start check waits until it sees the
// new one.
func TestSettleWaitsForStaleView(t *testing.T) {
	requireBaseImage(t)
	project := protectedProject(t)
	size := func() string {
		out, err := docker.RunOffline(baseImage, os.Getuid(), os.Getgid(), []string{project}, project, "stat", "-c", "%s", ".envrc")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	if got := size(); got != "10" {
		t.Fatalf(".envrc in the VM: %s bytes", got)
	}
	if err := replace(project, ".envrc", "use nix --impure\n"); err != nil {
		t.Fatal(err)
	}
	sess := &session{project: project, layout: mask.Layout{Mounts: []mask.Mount{{Path: ".envrc"}, {Path: ".env", Mask: true}, {Path: ".git", Dir: true, Pin: true}}}}
	start := time.Now()
	if err := sess.settle(); err != nil {
		t.Fatal(err)
	}
	t.Logf("settled after %s", time.Since(start).Round(time.Millisecond))
	if got := size(); got != "17" {
		t.Errorf(".envrc in the VM after the check: %s bytes, want the host's 17", got)
	}
}

// A protected file replaced on the host just before a session, while the
// Docker VM still has the old one cached, is read-only in the session.
// Here, kota's own setup before the first mount takes about as long as
// the VM caches the entry, so this passes even without the pre-start
// check; TestSettleWaitsForStaleView shows the check at work.
func TestReplacedJustBeforeSession(t *testing.T) {
	requireBaseImage(t)
	project := protectedProject(t)
	sa := sessionArgs{agent: "opencode", gatewayURL: stubModelGateway(t), dir: project, shell: true}
	// The VM caches .envrc.
	if _, err := docker.RunOffline(baseImage, os.Getuid(), os.Getgid(), []string{project}, project, "stat", ".envrc"); err != nil {
		t.Fatal(err)
	}
	if err := replace(project, ".envrc", "use nix\n"); err != nil {
		t.Fatal(err)
	}
	var out string
	withSession(t, sa, func(*sandbox.Compose) {}, func(_ *session, ds *docker.Session) {
		var err error
		if out, err = ds.Capture("bash", "-c", inPlace); err != nil {
			t.Fatal(err)
		}
	})
	checkInPlace(t, out)
	checkAfter(t, project)
	if b, _ := os.ReadFile(filepath.Join(project, ".envrc")); string(b) != "use nix\n" {
		t.Errorf(".envrc = %q", b)
	}
}

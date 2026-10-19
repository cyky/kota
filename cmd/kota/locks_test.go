//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kota/internal/hostlock"
	"kota/internal/sandbox"
)

// A lock list left by a killed session is unlocked when the next command
// takes the project, before it plans its mounts.
func TestLeftoverLocksUnlocked(t *testing.T) {
	project := fakeHome(t, map[string]string{".envrc": "use flake\n"})
	list, err := lockListFile(project)
	if err != nil {
		t.Fatal(err)
	}
	if n, failed, err := hostlock.Lock(project, []string{".envrc"}, list); err != nil || n != 1 || len(failed) > 0 {
		t.Fatalf("Lock = %d, %v, %v", n, failed, err)
	}
	t.Cleanup(func() { hostlock.Unlock(list) })
	envrc := filepath.Join(project, ".envrc")
	tmp := envrc + ".tmp"
	if err := os.WriteFile(tmp, []byte("use nix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, envrc); err == nil || !strings.Contains(err.Error(), "operation not permitted") {
		t.Fatalf("rename over the locked .envrc = %v", err)
	}

	lease, err := lockProject(sessionArgs{dir: project}, "run")
	if err != nil {
		t.Fatal(err)
	}
	releaseProject(lease)
	if err := os.Rename(tmp, envrc); err != nil {
		t.Errorf(".envrc still locked: %v", err)
	}
	if _, err := os.Lstat(list); !os.IsNotExist(err) {
		t.Errorf("lock list left: %v", err)
	}
}

// A session locks the files its mounts lie over, says how many, and
// unlocks them in its cleanup, before it removes placeholders.
func TestSessionLocksFiles(t *testing.T) {
	project := fakeHome(t, map[string]string{".envrc": "use flake\n", ".env": "KEY=1\n"})
	sess, err := prepareTestSession(sessionArgs{agent: "opencode", gatewayURL: defaultGateway, dir: project}, sandbox.ModeRun)
	if err != nil {
		t.Fatal(err)
	}
	cleaned := false
	t.Cleanup(func() {
		if !cleaned {
			sess.cleanup()
		}
	})
	// .envrc, .env, .git/config, .git/commondir (seeded), and placeholders
	// for .kotaignore, kota.yaml and kota.Dockerfile.
	if sess.locked != 7 {
		t.Errorf("locked %d files, want 7 (layout files %v)", sess.locked, sess.layout.Files())
	}
	var out strings.Builder
	sess.printWarnings(&out)
	if !strings.Contains(out.String(), "note: 7 protected and masked files are locked on the host until the session ends") {
		t.Errorf("no lock note:\n%s", out.String())
	}
	for _, rel := range []string{".env", ".kotaignore", ".git/commondir"} {
		if err := os.Remove(filepath.Join(project, rel)); err == nil {
			t.Errorf("%s removed during the session", rel)
		}
	}
	sess.cleanup()
	cleaned = true
	for _, rel := range []string{".kotaignore", "kota.yaml", ".git/commondir"} {
		if _, err := os.Lstat(filepath.Join(project, rel)); !os.IsNotExist(err) {
			t.Errorf("%s left: %v", rel, err)
		}
	}
	if err := os.WriteFile(filepath.Join(project, ".env"), []byte("KEY=2\n"), 0o644); err != nil {
		t.Errorf(".env still locked: %v", err)
	}
}

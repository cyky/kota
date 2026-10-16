package nohistory

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"kota/internal/changereport"
)

func TestBranchName(t *testing.T) {
	start := time.Date(2026, 10, 16, 14, 32, 5, 0, time.Local)
	if got := BranchName("pi", start); got != "refs/heads/kota/pi-2026-10-16T143205" {
		t.Errorf("branch = %s", got)
	}
}

func TestUnreturned(t *testing.T) {
	a, b, c := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	before := changereport.Refs{Head: a, Tips: map[string]string{
		"refs/heads/main": a, "refs/heads/old": a, "refs/tags/v1": a,
	}}
	after := changereport.Refs{Head: b, Tips: map[string]string{
		"refs/heads/main": b, // moved with HEAD: returned
		"refs/heads/wip":  c, // new elsewhere
		"refs/heads/side": b, // new at HEAD: returned
		"refs/tags/v1":    a, // unchanged
		"refs/tags/v2":    b, // new tag: tags are never returned
	}}
	got := Unreturned(before, after)
	if want := []string{"branch wip", "tag v2"}; !slices.Equal(got, want) {
		t.Errorf("unreturned = %q, want %q (old was deleted, which loses nothing)", got, want)
	}
}

func TestUncommittedLeavesOutConfigFiles(t *testing.T) {
	var mounts []string
	var cmd []string
	run := func(m []string, dir string, c ...string) (string, error) {
		mounts, cmd = m, c
		return " M src/a.go\n M kota.yaml\n?? new.txt\n", nil
	}
	got, err := Uncommitted("/copies/app", "/sessions/x/git-excludes", run)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{" M src/a.go", "?? new.txt"}; !slices.Equal(got, want) {
		t.Errorf("uncommitted = %q, want %q", got, want)
	}
	if !slices.Equal(mounts, []string{"/copies/app", "/sessions/x/git-excludes"}) ||
		!slices.Contains(cmd, "core.excludesFile=/sessions/x/git-excludes") {
		t.Errorf("mounts %q, command %q", mounts, cmd)
	}
}

func TestCommonDir(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := commonDir(repo); err != nil || got != filepath.Join(repo, ".git") {
		t.Errorf("repository: %s, %v", got, err)
	}
	wt := t.TempDir()
	gitDir := filepath.Join(repo, ".git", "worktrees", "wt")
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644)
	os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o644)
	if got, err := commonDir(wt); err != nil || got != filepath.Join(repo, ".git") {
		t.Errorf("linked worktree: %s, %v", got, err)
	}
}

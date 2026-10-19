package nohistory

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"kota/internal/changereport"
	"kota/internal/offlinegit"
)

func TestBranchName(t *testing.T) {
	start := time.Date(2026, 10, 16, 14, 32, 5, 0, time.Local)
	if got := branchName("pi", start); got != "refs/heads/kota/pi-2026-10-16T143205" {
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
	got := unreturned(before, after)
	if want := []string{"branch wip", "tag v2"}; !slices.Equal(got, want) {
		t.Errorf("unreturned = %q, want %q (old was deleted, which loses nothing)", got, want)
	}
}

func TestUncommittedLeavesOutConfigFiles(t *testing.T) {
	var job offlinegit.Job
	run := func(j offlinegit.Job) (string, error) {
		job = j
		return " M src/a.go\n M kota.yaml\n?? new.txt\n", nil
	}
	got, err := uncommittedChanges("/copies/app", "/sessions/x/git-excludes", run)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{" M src/a.go", "?? new.txt"}; !slices.Equal(got, want) {
		t.Errorf("uncommitted = %q, want %q", got, want)
	}
	if !slices.Equal(job.ReadOnly, []string{"/copies/app", "/sessions/x/git-excludes"}) || job.Writable != "" ||
		!slices.Contains(job.Cmd, "core.excludesFile=/sessions/x/git-excludes") {
		t.Errorf("job %+v", job)
	}
}

// The import writes only the original's common git dir, which for a
// linked worktree lies outside it.
func TestImportWritesOnlyTheCommonDir(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git", "worktrees", "wt")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wt := t.TempDir()
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644)
	os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o644)
	id := strings.Repeat("a", 40)
	for _, project := range []string{repo, wt} {
		var job offlinegit.Job
		run := func(j offlinegit.Job) (string, error) { job = j; return "returned " + id + " 1\n", nil }
		r, err := importBundle(project, "/sessions/x/return-1/return.bundle", id, "refs/heads/kota/pi-x", run)
		if err != nil || r.Commit != id {
			t.Fatalf("%s: %+v, %v", project, r, err)
		}
		if want := filepath.Join(repo, ".git"); job.Writable != want || job.Dir != want ||
			!slices.Equal(job.ReadOnly, []string{"/sessions/x/return-1"}) {
			t.Errorf("%s: job %+v, want %s writable", project, job, want)
		}
	}
}

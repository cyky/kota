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
	"kota/internal/projectskills"
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

// TestUncommittedLeavesOutConfigFiles checks kota's own files never count
// as the Session's work, and read-only Project skills don't either, while
// writable ones do unless the Session left them as copied.
func TestUncommittedLeavesOutConfigFiles(t *testing.T) {
	const status = " M src/a.go\n M kota.yaml\n?? new.txt\n M .agents/skills/deploy/SKILL.md\n?? .agents/skills/new/\n?? .agents/notes.md\n" +
		"R  .agents/skills/a -> src/a\n?? \".agents/skills/caf\\303\\251/SKILL.md\"\n"
	skillLines := []string{" M .agents/skills/deploy/SKILL.md", "?? .agents/skills/new/"}
	// A rename out of the skills is the agent's; a quoted skill path isn't.
	rest := []string{"?? .agents/notes.md", "R  .agents/skills/a -> src/a"}
	quoted := `?? ".agents/skills/caf\303\251/SKILL.md"`
	all := slices.Concat([]string{" M src/a.go", "?? new.txt"}, skillLines, rest, []string{quoted})
	cases := map[projectskills.Mode][]string{
		projectskills.Off:      all,
		projectskills.ReadOnly: slices.Concat([]string{" M src/a.go", "?? new.txt"}, rest),
		// Without a record of what was copied, every skill line is kept.
		projectskills.Writable: all,
	}
	for mode, want := range cases {
		var job offlinegit.Job
		run := func(j offlinegit.Job) (string, error) {
			job = j
			return status, nil
		}
		got, err := uncommittedChanges("/copies/app", "/sessions/x/git-excludes", run, Copied{Skills: mode})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%v: uncommitted\n got %q\nwant %q", mode, got, want)
		}
		if !slices.Equal(job.ReadOnly, []string{"/copies/app", "/sessions/x/git-excludes"}) || job.Writable != "" ||
			!slices.Contains(job.Cmd, "core.excludesFile=/sessions/x/git-excludes") {
			t.Errorf("job %+v", job)
		}
	}
}

// TestUncommittedWritableSkills checks the user's uncommitted skills,
// copied in and left alone, aren't the Session's work, while the skills
// the agent changed, added or deleted are.
func TestUncommittedWritableSkills(t *testing.T) {
	copy := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(copy, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".agents/skills/user/SKILL.md", "the user's, uncommitted\n")
	write(".agents/skills/deploy/SKILL.md", "the user's edit\n")
	write(".agents/skills/gone/SKILL.md", "x\n")
	files, err := changereport.SnapshotMatching(copy, []string{projectskills.Dir + "/"})
	if err != nil {
		t.Fatal(err)
	}
	copied := Copied{Skills: projectskills.Writable, skills: files.Digests()}
	// The agent's work.
	write(".agents/skills/deploy/SKILL.md", "the agent's edit\n")
	write(".agents/skills/added/SKILL.md", "new\n")
	if err := os.RemoveAll(filepath.Join(copy, ".agents/skills/gone")); err != nil {
		t.Fatal(err)
	}
	const status = "?? .agents/skills/user/SKILL.md\n M .agents/skills/deploy/SKILL.md\n?? .agents/skills/added/SKILL.md\n D .agents/skills/gone/SKILL.md\n"
	var job offlinegit.Job
	run := func(j offlinegit.Job) (string, error) { job = j; return status, nil }
	got, err := uncommittedChanges(copy, "/sessions/x/git-excludes", run, copied)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{" M .agents/skills/deploy/SKILL.md", "?? .agents/skills/added/SKILL.md", " D .agents/skills/gone/SKILL.md"}
	if !slices.Equal(got, want) {
		t.Errorf("uncommitted\n got %q\nwant %q", got, want)
	}
	if !slices.Contains(job.Cmd, "--untracked-files=all") {
		t.Errorf("untracked folders aren't listed file by file: %q", job.Cmd)
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

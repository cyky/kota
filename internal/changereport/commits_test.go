package changereport

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"kota/internal/offlinegit"
)

// localRun runs the job directly, in place of the offline container: tests
// only point it at repositories they made.
func localRun(j offlinegit.Job) (string, error) {
	c := exec.Command(j.Cmd[0], j.Cmd[1:]...)
	c.Dir = j.Dir
	out, err := c.Output()
	return string(out), err
}

func isolateGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func refs(t *testing.T, dir string) Refs {
	t.Helper()
	r, ok, err := ReadRefs(dir, localRun)
	if err != nil || !ok {
		t.Fatalf("ReadRefs: ok=%v err=%v", ok, err)
	}
	return r
}

func history(t *testing.T, dir string, before, after Refs) History {
	t.Helper()
	h, err := ReadHistory(dir, localRun, before, after)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func subjects(h History) []string {
	var out []string
	for _, c := range h.Commits {
		out = append(out, c.Subject)
	}
	return out
}

// repo has one commit with .npmrc and README.
func repo(t *testing.T) string {
	t.Helper()
	isolateGit(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, ".npmrc", "token=secret\n")
	write(t, dir, "README", "hi\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "start")
	return dir
}

func TestReadRefsNotARepo(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := ReadRefs(dir, localRun); ok || err != nil {
		t.Errorf("no .git: ok=%v err=%v, want false, nil", ok, err)
	}
	write(t, dir, ".git", "gitdir: /elsewhere\n")
	if _, ok, err := ReadRefs(dir, localRun); ok || err != nil {
		t.Errorf(".git file: ok=%v err=%v, want false, nil", ok, err)
	}
}

func TestReadRefsUnborn(t *testing.T) {
	isolateGit(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	r := refs(t, dir)
	if r.Head != "" || len(r.Tips) != 0 {
		t.Errorf("new repository: %+v, want no refs", r)
	}
}

func TestReadHistoryNothingChanged(t *testing.T) {
	dir := repo(t)
	before := refs(t, dir)
	h := history(t, dir, before, refs(t, dir))
	if len(h.Commits) != 0 || h.Rewritten {
		t.Errorf("got %+v, want nothing", h)
	}
}

func TestReadHistoryFiles(t *testing.T) {
	dir := repo(t)
	before := refs(t, dir)
	write(t, dir, ".npmrc", "")
	write(t, dir, ".env", "")
	write(t, dir, "README", "changed\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "agent work")
	git(t, dir, "rm", "-q", "README")
	git(t, dir, "commit", "-q", "-m", "drop readme")
	h := history(t, dir, before, refs(t, dir))
	if h.Rewritten {
		t.Error("Rewritten set for commits on top")
	}
	if got := subjects(h); !slices.Equal(got, []string{"drop readme", "agent work"}) {
		t.Fatalf("subjects = %q", got)
	}
	want := []File{{".env", 'A', false}, {".npmrc", 'M', true}, {"README", 'M', false}}
	if got := h.Commits[1].Files; !slices.Equal(got, want) {
		t.Errorf("files = %+v, want %+v", got, want)
	}
	if got := h.Commits[0].Files; !slices.Equal(got, []File{{"README", 'D', false}}) {
		t.Errorf("files = %+v", got)
	}
}

func TestReadHistoryOtherBranchAndDetached(t *testing.T) {
	dir := repo(t)
	before := refs(t, dir)
	git(t, dir, "switch", "-q", "-c", "wip")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "on wip")
	git(t, dir, "switch", "-q", "--detach", "main")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "detached")
	after := refs(t, dir)
	h := history(t, dir, before, after)
	got := subjects(h)
	slices.Sort(got)
	if !slices.Equal(got, []string{"detached", "on wip"}) {
		t.Errorf("subjects = %q, want the wip and detached commits", got)
	}
	if h.Rewritten {
		t.Error("Rewritten set, but HEAD still contains main")
	}
	if got := HistoryLines(before, after, h); got[0] != "new branch wip" {
		t.Errorf("first line %q, want the new branch", got[0])
	}
}

func TestReadHistoryAmend(t *testing.T) {
	dir := repo(t)
	before := refs(t, dir)
	write(t, dir, ".npmrc", "")
	git(t, dir, "commit", "-q", "-a", "--amend", "-m", "start, amended")
	after := refs(t, dir)
	h := history(t, dir, before, after)
	if !h.Rewritten {
		t.Error("amend not reported as rewritten")
	}
	if got := subjects(h); !slices.Equal(got, []string{"start, amended"}) {
		t.Errorf("subjects = %q", got)
	}
	lines := HistoryLines(before, after, h)
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "git reset --hard "+before.Head[:7]) }) {
		t.Errorf("no recovery hint in %q", lines)
	}
}

func TestReadHistoryResetBack(t *testing.T) {
	dir := repo(t)
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "second")
	before := refs(t, dir)
	git(t, dir, "reset", "-q", "--hard", "HEAD~")
	h := history(t, dir, before, refs(t, dir))
	if !h.Rewritten || len(h.Commits) != 0 {
		t.Errorf("got %+v, want rewritten with no commits", h)
	}
}

func TestReadHistoryFromUnborn(t *testing.T) {
	isolateGit(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	before := refs(t, dir)
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "first")
	h := history(t, dir, before, refs(t, dir))
	if h.Rewritten || !slices.Equal(subjects(h), []string{"first"}) {
		t.Errorf("got %+v", h)
	}
}

func TestReadHistoryAllDeleted(t *testing.T) {
	dir := repo(t)
	before := refs(t, dir)
	git(t, dir, "switch", "-q", "--orphan", "fresh")
	git(t, dir, "branch", "-q", "-D", "main")
	h := history(t, dir, before, refs(t, dir))
	if !h.Rewritten || len(h.Commits) != 0 {
		t.Errorf("got %+v, want rewritten with no commits", h)
	}
}

func TestParseLogSubjectWithMarker(t *testing.T) {
	id := strings.Repeat("a", 40)
	out := "\x01" + id + "\x00evil\x01" + strings.Repeat("b", 40) + "\x00"
	commits, err := parseLog(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 1 || commits[0].ID != id {
		t.Errorf("got %+v, want one commit", commits)
	}
	if _, err := parseLog("junk"); err == nil {
		t.Error("junk parsed")
	}
}

func TestCheckCommits(t *testing.T) {
	commits := []Commit{
		{ID: "2222222", Files: []File{
			{".npmrc", 'D', false},
			{"package.json", 'M', false},
		}},
		{ID: "1111111", Files: []File{
			{".npmrc", 'M', true},
			{".envrc", 'A', false},
			{".env", 'A', false},
			{"deploy/.aws/credentials", 'A', false},
			{".claude/settings.local.json", 'A', false},
			{".github/workflows/ci.yml", 'M', false},
			{"node_modules/x/package.json", 'M', false},
			{"main.go", 'M', false},
		}},
	}
	p := CommitPaths{
		Placeholders: []string{".envrc"},
		Masked:       []string{".npmrc", ".env", "deploy/.aws/"},
		Ignored:      func(rel string) bool { return strings.HasPrefix(rel, ".claude/") },
		Reported:     []Change{{"package.json", Modified}},
	}
	got := CheckCommits(commits, p)
	type row struct {
		path    string
		reason  Reason
		changes string
	}
	var rows []row
	for _, w := range got {
		var cs []string
		for _, c := range w.Changes {
			cs = append(cs, c.Kind+"@"+c.Commit)
		}
		rows = append(rows, row{w.Path, w.Reason, strings.Join(cs, ",")})
	}
	want := []row{
		{".envrc", Placeholder, "added@1111111"},
		{".env", Masked, "added@1111111"},
		{".npmrc", Masked, "emptied@1111111,deleted@2222222"},
		{"deploy/.aws/credentials", Masked, "added@1111111"},
		{".claude/settings.local.json", GloballyIgnored, "added@1111111"},
		{".github/workflows/ci.yml", HiddenWatched, "modified@1111111"},
	}
	if !slices.Equal(rows, want) {
		t.Errorf("CheckCommits =\n%v\nwant\n%v", rows, want)
	}

	lines := WarningLines(got)
	for _, want := range []string{
		"  git rm --cached -- .envrc .env deploy/.aws/credentials",
		"  git add -- .npmrc",
		"  git commit",
	} {
		if !slices.Contains(lines, want) {
			t.Errorf("WarningLines lacks %q:\n%s", want, strings.Join(lines, "\n"))
		}
	}
	if !slices.ContainsFunc(lines, func(l string) bool {
		return strings.HasPrefix(l, ".npmrc  masked") && strings.HasSuffix(l, "emptied in 1111111, deleted in 2222222")
	}) {
		t.Errorf("no .npmrc line in:\n%s", strings.Join(lines, "\n"))
	}
}

func TestIsWatched(t *testing.T) {
	for rel, want := range map[string]bool{
		"package.json":                true,
		"web/package.json":            true,
		".github/workflows/ci.yml":    true,
		"app/.vscode/tasks.json":      true,
		".vscode":                     true,
		"node_modules/x/package.json": false,
		"main.go":                     false,
		"tools/run.sh":                true, // extra
	} {
		if got := IsWatched(rel, []string{"tools/run.sh"}); got != want {
			t.Errorf("IsWatched(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestHistoryLinesCapAndClean(t *testing.T) {
	var h History
	for i := 0; i < 25; i++ {
		h.Commits = append(h.Commits, Commit{ID: strings.Repeat("c", 40), Subject: "fix\x1b[2Jthings"})
	}
	lines := HistoryLines(Refs{}, Refs{}, h)
	if lines[0] != "25 new commit(s):" || len(lines) != 22 || lines[21] != "  ... and 5 more" {
		t.Errorf("lines = %q", lines)
	}
	if strings.Contains(lines[1], "\x1b") {
		t.Errorf("control character kept: %q", lines[1])
	}
}

func TestShellWords(t *testing.T) {
	got := shellWords([]string{"a/b.txt", "it's here", "x\ny"})
	want := ` a/b.txt 'it'\''s here' "x\ny"`
	if got != want {
		t.Errorf("shellWords = %s, want %s", got, want)
	}
}

func TestRefsEqual(t *testing.T) {
	a := Refs{Head: "x", Tips: map[string]string{"refs/heads/main": "x"}}
	b := Refs{Head: "x", Tips: map[string]string{"refs/heads/main": "x"}}
	if !a.Equal(b) {
		t.Error("equal refs differ")
	}
	b.Tips["refs/tags/v1"] = "x"
	if a.Equal(b) {
		t.Error("new tag not noticed")
	}
}

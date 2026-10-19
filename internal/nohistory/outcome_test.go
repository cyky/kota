package nohistory

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"kota/internal/changereport"
	"kota/internal/offlinegit"
)

// refs is the copy's refs, as a *changereport.Result gives them.
type refs struct {
	before, after changereport.Refs
	ok            bool
}

func (r refs) Refs() (changereport.Refs, changereport.Refs, bool) { return r.before, r.after, r.ok }

// returnGit answers the git Return runs: status in the copy, the export
// and the import, each with its output or error.
type returnGit struct {
	status, importOut               string
	statusErr, exportErr, importErr error
	exported, imported              bool
}

func (g *returnGit) run(j offlinegit.Job) (string, error) {
	cmd := strings.Join(j.Cmd, " ")
	switch {
	case strings.Contains(cmd, "status --porcelain"):
		return g.status, g.statusErr
	case strings.Contains(cmd, "bundle create"):
		g.exported = true
		return "", g.exportErr
	case strings.Contains(cmd, "fetch -q"):
		g.imported = true
		return g.importOut, g.importErr
	}
	return "", errors.New("unexpected job: " + cmd)
}

func TestReturn(t *testing.T) {
	a, b, c := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	main := func(head string, extra ...string) changereport.Refs {
		r := changereport.Refs{Head: head, Tips: map[string]string{"refs/heads/main": head}}
		for i := 0; i+1 < len(extra); i += 2 {
			r.Tips[extra[i]] = extra[i+1]
		}
		return r
	}
	moved := refs{main(a), main(b), true}
	start := time.Date(2026, 10, 19, 12, 0, 0, 0, time.Local)
	const branch = "kota/pi-2026-10-19T120000"
	cases := []struct {
		name string
		refs refs
		git  returnGit
		// unremovable makes the copy impossible to remove.
		unremovable bool
		// branch is the Returned branch's line, or after "no branch brought
		// back: ".
		branch, none string
		keep         []string
		exported     bool
		// report lines that must be there too.
		lines []string
	}{
		{
			name: "returned and removed", refs: moved, git: returnGit{importOut: "returned " + b + " 1\n"},
			branch: "returned branch " + branch + " (1 commit(s), at bbbbbbbbbbbb)", exported: true,
			lines: []string{"the copy was removed"},
		},
		{
			name: "no new commits", refs: refs{main(a), main(a), true},
			none: "no commits to bring back", lines: []string{"the copy was removed"},
		},
		{
			name: "HEAD with no commit", refs: refs{main(a), changereport.Refs{Tips: map[string]string{}}, true},
			none: "the copy's HEAD has no commit", keep: []string{"the copy's HEAD has no commit"},
		},
		{
			name: "rewritten history", refs: moved, git: returnGit{importOut: "rewritten\n"},
			none: "history rewritten, not brought back", keep: []string{"its commits weren't brought back"}, exported: true,
		},
		{
			name: "failed export", refs: moved, git: returnGit{exportErr: errors.New("disk full")},
			none: "could not bring back: export the copy's commits: disk full", keep: []string{"its commits weren't brought back"}, exported: true,
		},
		{
			name: "failed import", refs: moved, git: returnGit{importErr: errors.New("docker is not running")},
			none: "could not bring back: import into", keep: []string{"its commits weren't brought back"}, exported: true,
		},
		{
			name: "uncommitted changes", refs: moved, git: returnGit{status: " M README\n?? notes.txt\n", importOut: "returned " + b + " 1\n"},
			branch: "returned branch " + branch, keep: []string{"uncommitted changes"}, exported: true,
			lines: []string{"\n   M README\n  ?? notes.txt\n", "review them before running git there"},
		},
		{
			name: "unreadable status", refs: moved, git: returnGit{statusErr: errors.New("docker is not running"), importOut: "returned " + b + " 1\n"},
			branch: "returned branch " + branch, keep: []string{"could not read its status: git status in the copy: docker is not running"}, exported: true,
		},
		{
			name: "refs not returned", refs: refs{main(a), main(b, "refs/heads/wip", c, "refs/tags/v2", b), true}, git: returnGit{importOut: "returned " + b + " 1\n"},
			branch: "returned branch " + branch, keep: []string{"branches or tags not brought back"}, exported: true,
			lines: []string{"not brought back: branch wip, tag v2\n"},
		},
		{
			name: "unreadable commits", refs: refs{ok: false},
			none: "could not read the copy's commits", keep: []string{"could not read the copy's commits"},
		},
		{
			name: "failed removal", refs: refs{main(a), main(a), true}, unremovable: true,
			none: "no commits to bring back", keep: []string{"could not remove it: "},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copy, original, scratch := t.TempDir(), t.TempDir(), t.TempDir()
			if err := os.Mkdir(filepath.Join(original, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.unremovable {
				dir := filepath.Join(copy, "readonly")
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "file"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
				os.Chmod(dir, 0o555)
				t.Cleanup(func() { os.Chmod(dir, 0o755) })
			}
			git := tc.git
			o := Return(copy, original, tc.refs, "pi", start, git.run, filepath.Join(scratch, "git-excludes"), scratch)

			if git.exported != tc.exported || git.imported != (tc.exported && tc.git.exportErr == nil) {
				t.Errorf("exported %v, imported %v", git.exported, git.imported)
			}
			for i, want := range tc.keep {
				if i >= len(o.keep) || !strings.HasPrefix(o.keep[i], want) {
					t.Errorf("keep = %q, want %q", o.keep, tc.keep)
					break
				}
			}
			if len(o.keep) != len(tc.keep) {
				t.Errorf("keep = %q, want %q", o.keep, tc.keep)
			}
			_, err := os.Stat(copy)
			if removed := os.IsNotExist(err); removed != (len(tc.keep) == 0) {
				t.Errorf("copy removed: %v, kept for %q", removed, o.keep)
			}

			var out strings.Builder
			o.Report(&out)
			report := out.String()
			lines := tc.lines
			if tc.branch != "" {
				lines = append(lines, tc.branch)
			} else {
				lines = append(lines, "no-history: no branch brought back: "+tc.none)
			}
			if len(tc.keep) > 0 {
				lines = append(lines, "copy kept at "+copy+" (")
			}
			for _, want := range lines {
				if !strings.Contains(report, want) {
					t.Errorf("report lacks %q:\n%s", want, report)
				}
			}
		})
	}
}

// A kept copy's report lists at most 20 uncommitted changes.
func TestReturnReportCapsUncommitted(t *testing.T) {
	var status []string
	for i := range 25 {
		status = append(status, "?? f"+strings.Repeat("x", i))
	}
	a := strings.Repeat("a", 40)
	same := changereport.Refs{Head: a, Tips: map[string]string{"refs/heads/main": a}}
	git := returnGit{status: strings.Join(status, "\n") + "\n"}
	o := Return(t.TempDir(), t.TempDir(), refs{same, same, true}, "pi", time.Now(), git.run, "x", t.TempDir())
	var out strings.Builder
	o.Report(&out)
	lines := strings.Split(out.String(), "\n")
	if n := slices.IndexFunc(lines, func(l string) bool { return l == "  ... and 5 more" }); n < 0 {
		t.Errorf("report:\n%s", out.String())
	}
}

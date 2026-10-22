package nohistory

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"kota/internal/changereport"
	"kota/internal/hostdata"
	"kota/internal/offlinegit"
	"kota/internal/projectskills"
)

// Refs are the copy's refs at the start and the end of the Session, as a
// *changereport.Result has them. ok is false when they couldn't be read.
type Refs interface {
	Refs() (before, after changereport.Refs, ok bool)
}

// Outcome is what Return did with a No-history copy.
type Outcome struct {
	copy string
	// branch is the Returned branch, when one was made.
	branch returnedBranch
	// none is why no branch was made, when none was.
	none string
	// unreturned are branches and tags in the copy that weren't brought
	// back, uncommitted its uncommitted changes.
	unreturned, uncommitted []string
	// keep are the reasons the copy is kept; it was removed when there are
	// none.
	keep []string
}

// Return brings the copy's HEAD back into original as the Returned branch
// of a Session of name (the agent, or "shell") started at start, and
// removes the copy unless something in it would be lost: uncommitted
// changes, branches or tags not brought back, or commits that couldn't
// be. commits are the copy's refs; excludes is the Session's excludes
// file; the bundle goes in a new directory under scratch; copied is what
// Clone took from the project.
//
// Call it once the containers are gone and the copy's files are unlocked,
// before the Session's cleanup: git reads the seeded files in the copy's
// .git. git runs in offline containers only: in the copy only with the
// bundle's directory writable, and in original without the copy.
func Return(copy, original string, commits Refs, name string, start time.Time, run offlinegit.Runner, excludes, scratch string, copied Copied) Outcome {
	o := Outcome{copy: copy}
	before, after, ok := commits.Refs()
	if !ok {
		o.none = "could not read the copy's commits"
		o.keep = append(o.keep, o.none)
		return o
	}
	uncommitted, err := uncommittedChanges(copy, excludes, run, copied)
	if err != nil {
		o.keep = append(o.keep, fmt.Sprintf("could not read its status: %v", err))
	} else if o.uncommitted = uncommitted; len(uncommitted) > 0 {
		o.keep = append(o.keep, "uncommitted changes")
	}
	if o.unreturned = unreturned(before, after); len(o.unreturned) > 0 {
		o.keep = append(o.keep, "branches or tags not brought back")
	}
	base, head := before.Head, after.Head
	switch {
	case head == base:
		o.none = "no commits to bring back"
	case head == "":
		o.none = "the copy's HEAD has no commit"
		o.keep = append(o.keep, o.none)
	default:
		o.branch, err = bringBack(copy, original, base, branchName(name, start), run, scratch)
		if errors.Is(err, errRewritten) {
			o.none = err.Error()
		} else if err != nil {
			o.none = fmt.Sprintf("could not bring back: %v", err)
		}
		if err != nil {
			o.keep = append(o.keep, "its commits weren't brought back")
		}
	}
	if len(o.keep) == 0 {
		if err := os.RemoveAll(copy); err != nil {
			o.keep = append(o.keep, fmt.Sprintf("could not remove it: %v", err))
		}
	}
	return o
}

// bringBack exports the copy's new commits to a bundle in a new directory
// under scratch and imports them into original as branch.
func bringBack(copy, original, base, branch string, run offlinegit.Runner, scratch string) (returnedBranch, error) {
	dir, err := os.MkdirTemp(scratch, "return-")
	if err != nil {
		return returnedBranch{}, err
	}
	bundle := filepath.Join(dir, "return.bundle")
	if err := export(copy, base, bundle, run); err != nil {
		return returnedBranch{}, err
	}
	return importBundle(original, bundle, base, branch, run)
}

// maxUncommitted is how many uncommitted changes in a kept copy the
// report lists.
const maxUncommitted = 20

// Report prints the return part of the Change report.
func (o Outcome) Report(w io.Writer) {
	if o.branch.Branch != "" {
		fmt.Fprintf(w, "returned branch %s (%d commit(s), at %s); review it, then merge it yourself\n",
			strings.TrimPrefix(o.branch.Branch, "refs/heads/"), o.branch.Commits, o.branch.Commit[:min(12, len(o.branch.Commit))])
	} else {
		fmt.Fprintf(w, "no-history: no branch brought back: %s\n", o.none)
	}
	if len(o.unreturned) > 0 {
		fmt.Fprintf(w, "not brought back: %s\n", strings.Join(o.unreturned, ", "))
	}
	if len(o.keep) == 0 {
		fmt.Fprintln(w, "the copy was removed")
		return
	}
	fmt.Fprintf(w, "copy kept at %s (%s)\n", o.copy, strings.Join(o.keep, "; "))
	for i, line := range o.uncommitted {
		if i == maxUncommitted {
			fmt.Fprintf(w, "  ... and %d more\n", len(o.uncommitted)-maxUncommitted)
			break
		}
		fmt.Fprintln(w, "  "+line)
	}
	fmt.Fprintln(w, "  git in the copy reads config and hooks the agent could write: review them before running git there; kota clean removes the copy")
}

// branchName is the Returned branch of a Session of name (the agent, or
// "shell") started at start, as a full ref.
func branchName(name string, start time.Time) string {
	return "refs/heads/kota/" + name + "-" + start.Format(hostdata.TimeFormat)
}

// uncommittedChanges returns the copy's uncommitted changes, as git status
// --porcelain lines, kota's own files left out: read-only Project skills,
// and writable ones the Session didn't change, are too. excludes is the
// Session's excludes file, so placeholders and masks aren't listed. Read it
// before the Session's cleanup: Docker's file sharing can show a removed
// seeded file as still there.
func uncommittedChanges(copy, excludes string, run offlinegit.Runner, copied Copied) ([]string, error) {
	skill, err := unchangedSkill(copy, copied)
	if err != nil {
		return nil, err
	}
	// Writable skills are told apart file by file, so an untracked folder
	// that holds them is listed file by file too.
	untracked := "--untracked-files=normal"
	if copied.Skills == projectskills.Writable {
		untracked = "--untracked-files=all"
	}
	out, err := offlinegit.Git(run, offlinegit.Job{ReadOnly: []string{copy, excludes}, Dir: copy},
		"-c", "core.excludesFile="+excludes, "status", "--porcelain", untracked)
	if err != nil {
		return nil, fmt.Errorf("git status in the copy: %w", err)
	}
	var changes []string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line != "" && !own(line, skill) {
			changes = append(changes, line)
		}
	}
	return changes, nil
}

// unchangedSkill returns whether a path in the copy's Project skills is
// kota's rather than the agent's: always when they were read-only, never
// when the copy didn't take them, and when writable, if nothing at or under
// the path differs from what Clone copied.
func unchangedSkill(copy string, copied Copied) (func(p string) bool, error) {
	switch {
	case copied.Skills == projectskills.ReadOnly:
		return func(string) bool { return true }, nil
	case copied.Skills != projectskills.Writable || copied.skills == nil:
		return func(string) bool { return false }, nil
	}
	files, err := changereport.SnapshotMatching(copy, []string{projectskills.Dir + "/"})
	if err != nil {
		return nil, fmt.Errorf("read the copy's %s: %w", projectskills.Dir, err)
	}
	now := files.Digests()
	return func(p string) bool {
		p = strings.TrimSuffix(p, "/")
		under := func(k string) bool { return k == p || strings.HasPrefix(k, p+"/") }
		for k, d := range now {
			if under(k) && copied.skills[k] != d {
				return false
			}
		}
		for k := range copied.skills {
			if _, ok := now[k]; under(k) && !ok {
				return false
			}
		}
		return true
	}, nil
}

// exportScript writes the commits on HEAD that aren't on $2 to the bundle
// $1. git runs in the copy, whose config the agent wrote, so the container
// can write nothing but the bundle's directory.
const exportScript = `set -e
g -c core.hooksPath=/dev/null bundle create -q "$1" HEAD "^$2"`

// export writes the commits on the copy's HEAD that its base doesn't have
// to the bundle file, whose directory must exist and be empty.
func export(copy, base, bundle string, run offlinegit.Runner) error {
	job := offlinegit.Job{ReadOnly: []string{copy}, Writable: filepath.Dir(bundle), Dir: copy}
	if _, err := offlinegit.Script(run, job, exportScript, bundle, base); err != nil {
		return fmt.Errorf("export the copy's commits: %w", err)
	}
	return nil
}

// importScript fetches the bundle $2's HEAD into the git dir $1 as the
// ref $4, then creates the branch $5 there if it descends from $3 and
// doesn't exist yet. git never runs in the copy here, and the original's
// hooks are off: fetch runs the reference-transaction hook. o is g in the
// original.
const importScript = `set -e
gitdir=$1 bundle=$2 base=$3 incoming=$4 branch=$5
o() { g -c core.hooksPath=/dev/null -c gc.auto=0 -c maintenance.auto=false \
  -c fetch.writeCommitGraph=false -c transfer.fsckObjects=true --git-dir="$gitdir" "$@"; }
o fetch -q --no-tags --no-write-fetch-head "$bundle" "HEAD:$incoming"
if ! o merge-base --is-ancestor "$base" "$incoming"; then
  o update-ref -d "$incoming"
  echo rewritten
  exit 0
fi
if o show-ref -q --verify "$branch"; then
  o update-ref -d "$incoming"
  echo exists
  exit 0
fi
# The empty old value refuses a branch created in the meantime.
o update-ref "$branch" "$incoming" ""
o update-ref -d "$incoming"
echo "returned $(o rev-parse "$branch") $(o rev-list --count "$base..$branch")"`

// errRewritten is returned by importBundle when the bundle's HEAD doesn't
// descend from the base: the agent reset or replaced the history.
var errRewritten = errors.New("history rewritten, not brought back")

// returnedBranch is a Returned branch.
type returnedBranch struct {
	// Branch is the full ref, Commit its tip.
	Branch, Commit string
	// Commits is how many commits it has that the base hasn't.
	Commits int
}

// importBundle brings the bundle's HEAD into project's repository as the
// full ref branch, if it descends from base and branch doesn't exist yet.
// Only the repository's common git dir is writable.
func importBundle(project, bundle, base, branch string, run offlinegit.Runner) (returnedBranch, error) {
	repo, err := repoOf(project)
	if err != nil {
		return returnedBranch{}, err
	}
	gitDir := repo.CommonDir
	incoming := "refs/kota-incoming/" + filepath.Base(strings.TrimPrefix(branch, "refs/heads/kota/"))
	job := offlinegit.Job{ReadOnly: []string{filepath.Dir(bundle)}, Writable: gitDir, Dir: gitDir}
	out, err := offlinegit.Script(run, job, importScript, gitDir, bundle, base, incoming, branch)
	if err != nil {
		return returnedBranch{}, fmt.Errorf("import into %s: %w", project, err)
	}
	result := strings.TrimSpace(out)
	switch result {
	case "rewritten":
		return returnedBranch{}, errRewritten
	case "exists":
		return returnedBranch{}, fmt.Errorf("branch %s already exists", strings.TrimPrefix(branch, "refs/heads/"))
	}
	var r returnedBranch
	_, err = fmt.Sscanf(result, "returned %s %d", &r.Commit, &r.Commits)
	if err != nil || !objectID.MatchString(r.Commit) {
		return returnedBranch{}, fmt.Errorf("import into %s: unexpected output %q", project, result)
	}
	r.Branch = branch
	return r, nil
}

var objectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// unreturned are the branches and tags the Session created or moved in
// the copy that aren't brought back: every tag, and every branch whose
// tip isn't HEAD. Deleted refs lose nothing.
func unreturned(before, after changereport.Refs) []string {
	var out []string
	for _, c := range changereport.RefChanges(before, after) {
		if c.Kind == changereport.Deleted {
			continue
		}
		if name, ok := strings.CutPrefix(c.Name, "refs/heads/"); ok {
			if after.Tips[c.Name] != after.Head {
				out = append(out, "branch "+name)
			}
		} else if name, ok := strings.CutPrefix(c.Name, "refs/tags/"); ok {
			out = append(out, "tag "+name)
		}
	}
	return out
}

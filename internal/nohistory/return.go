package nohistory

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"kota/internal/changereport"
	"kota/internal/hostdata"
	"kota/internal/offlinegit"
)

// BranchName is the Returned branch of a Session of name (the agent, or
// "shell") started at start, as a full ref.
func BranchName(name string, start time.Time) string {
	return "refs/heads/kota/" + name + "-" + start.Format(hostdata.TimeFormat)
}

// Uncommitted returns the copy's uncommitted changes, as git status
// --porcelain lines, kota's own files left out. excludes is the
// Session's excludes file, so placeholders and masks aren't listed. Read
// it before the Session's cleanup: Docker's file sharing can show a
// removed seeded file as still there.
func Uncommitted(copy, excludes string, run offlinegit.Runner) ([]string, error) {
	out, err := offlinegit.Git(run, offlinegit.Job{ReadOnly: []string{copy, excludes}, Dir: copy},
		"-c", "core.excludesFile="+excludes, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return nil, fmt.Errorf("git status in the copy: %w", err)
	}
	var changes []string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line != "" && !slices.Contains(ConfigFiles, strings.TrimSpace(line[min(3, len(line)):])) {
			changes = append(changes, line)
		}
	}
	return changes, nil
}

// exportScript writes the commits on HEAD that aren't on $2 to the bundle
// $1. git runs in the copy, whose config the agent wrote, so the container
// can write nothing but the bundle's directory.
const exportScript = `set -e
g -c core.hooksPath=/dev/null bundle create -q "$1" HEAD "^$2"`

// Export writes the commits on the copy's HEAD that its base doesn't have
// to the bundle file, whose directory must exist and be empty.
func Export(copy, base, bundle string, run offlinegit.Runner) error {
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

// ErrRewritten is returned by Import when the bundle's HEAD doesn't
// descend from the base: the agent reset or replaced the history.
var ErrRewritten = errors.New("history rewritten, not brought back")

// Returned is a Returned branch.
type Returned struct {
	// Branch is the full ref, Commit its tip.
	Branch, Commit string
	// Commits is how many commits it has that the base hasn't.
	Commits int
}

// Import brings the bundle's HEAD into project's repository as the full
// ref branch, if it descends from base and branch doesn't exist yet. Only
// the repository's common git dir is writable.
func Import(project, bundle, base, branch string, run offlinegit.Runner) (Returned, error) {
	repo, err := repoOf(project)
	if err != nil {
		return Returned{}, err
	}
	gitDir := repo.CommonDir
	incoming := "refs/kota-incoming/" + filepath.Base(strings.TrimPrefix(branch, "refs/heads/kota/"))
	job := offlinegit.Job{ReadOnly: []string{filepath.Dir(bundle)}, Writable: gitDir, Dir: gitDir}
	out, err := offlinegit.Script(run, job, importScript, gitDir, bundle, base, incoming, branch)
	if err != nil {
		return Returned{}, fmt.Errorf("import into %s: %w", project, err)
	}
	result := strings.TrimSpace(out)
	switch result {
	case "rewritten":
		return Returned{}, ErrRewritten
	case "exists":
		return Returned{}, fmt.Errorf("branch %s already exists", strings.TrimPrefix(branch, "refs/heads/"))
	}
	var r Returned
	_, err = fmt.Sscanf(result, "returned %s %d", &r.Commit, &r.Commits)
	if err != nil || !objectID.MatchString(r.Commit) {
		return Returned{}, fmt.Errorf("import into %s: unexpected output %q", project, result)
	}
	r.Branch = branch
	return r, nil
}

var objectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// Unreturned are the branches and tags the Session created or moved in
// the copy that aren't brought back: every tag, and every branch whose
// tip isn't HEAD. Deleted refs lose nothing.
func Unreturned(before, after changereport.Refs) []string {
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

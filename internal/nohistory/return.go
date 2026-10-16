package nohistory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"kota/internal/approval"
	"kota/internal/changereport"
	"kota/internal/hostdata"
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
func Uncommitted(copy, excludes string, run approval.Runner) ([]string, error) {
	out, err := run([]string{copy, excludes}, copy, "git", "-c", "safe.directory=*", "-c", "core.fsmonitor=false",
		"-c", "core.excludesFile="+excludes, "--no-optional-locks", "status", "--porcelain", "--untracked-files=normal")
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
g() { git -c 'safe.directory=*' -c core.fsmonitor=false -c core.hooksPath=/dev/null --no-optional-locks "$@"; }
g bundle create -q "$1" HEAD "^$2"`

// Export writes the commits on the copy's HEAD that its base doesn't have
// to the bundle file, whose directory must exist and be empty.
func Export(copy, base, bundle string, run WritableRunner) error {
	if _, err := run([]string{copy}, filepath.Dir(bundle), copy, "sh", "-c", exportScript, "sh", bundle, base); err != nil {
		return fmt.Errorf("export the copy's commits: %w", err)
	}
	return nil
}

// importScript fetches the bundle $2's HEAD into the git dir $1 as the
// ref $4, then creates the branch $5 there if it descends from $3 and
// doesn't exist yet. git never runs in the copy here, and the original's
// hooks are off: fetch runs the reference-transaction hook.
const importScript = `set -e
gitdir=$1 bundle=$2 base=$3 incoming=$4 branch=$5
g() { git -c 'safe.directory=*' -c core.fsmonitor=false -c core.hooksPath=/dev/null -c gc.auto=0 \
  -c maintenance.auto=false -c fetch.writeCommitGraph=false -c transfer.fsckObjects=true \
  --no-optional-locks --git-dir="$gitdir" "$@"; }
g fetch -q --no-tags --no-write-fetch-head "$bundle" "HEAD:$incoming"
if ! g merge-base --is-ancestor "$base" "$incoming"; then
  g update-ref -d "$incoming"
  echo rewritten
  exit 0
fi
if g show-ref -q --verify "$branch"; then
  g update-ref -d "$incoming"
  echo exists
  exit 0
fi
# The empty old value refuses a branch created in the meantime.
g update-ref "$branch" "$incoming" ""
g update-ref -d "$incoming"
echo "returned $(g rev-parse "$branch") $(g rev-list --count "$base..$branch")"`

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
func Import(project, bundle, base, branch string, run WritableRunner) (Returned, error) {
	gitDir, err := commonDir(project)
	if err != nil {
		return Returned{}, err
	}
	incoming := "refs/kota-incoming/" + filepath.Base(strings.TrimPrefix(branch, "refs/heads/kota/"))
	out, err := run([]string{filepath.Dir(bundle)}, gitDir, gitDir, "sh", "-c", importScript, "sh", gitDir, bundle, base, incoming, branch)
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

// commonDir is the git dir holding project's objects and refs: its .git
// directory, or for a linked worktree the common dir its .git file leads
// to. It is found without running git.
func commonDir(project string) (string, error) {
	dotGit := filepath.Join(project, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return dotGit, nil
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is a %s, not a directory or file", dotGit, info.Mode().Type())
	}
	gitDir, err := pointer(dotGit, "gitdir: ")
	if err != nil {
		return "", err
	}
	common, err := pointer(filepath.Join(gitDir, "commondir"), "")
	if errors.Is(err, os.ErrNotExist) {
		return gitDir, nil
	}
	return common, err
}

// pointer reads a file holding one path after prefix, as .git files and
// commondir do, made absolute against the file's directory.
func pointer(file, prefix string) (string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	p, ok := strings.CutPrefix(strings.TrimSpace(string(data)), prefix)
	if !ok || p == "" {
		return "", fmt.Errorf("%s: unexpected content", file)
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(filepath.Dir(file), p)
	}
	return filepath.Clean(p), nil
}

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

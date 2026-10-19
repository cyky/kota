package changereport

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"kota/internal/offlinegit"
)

// Refs are a repository's branch and tag tips and its HEAD commit.
type Refs struct {
	// Head is the commit HEAD points at, "" while the branch is unborn.
	Head string
	// Tips maps each full ref name under refs/heads and refs/tags to the
	// object it points at.
	Tips map[string]string
}

var objectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// ReadRefs records the refs of the repository at project. ok is false
// unless project has its own .git directory: otherwise git can't work
// inside the sandbox (a .git file points outside it), so nothing there can
// make commits.
//
// git runs offline: a repository the agent created has agent-written
// config, and git can run what config and .gitattributes name.
func ReadRefs(project string, run offlinegit.Runner) (refs Refs, ok bool, err error) {
	repo, ok, err := offlinegit.FindRepo(project)
	if err != nil || !ok || repo.Top != project || repo.Linked {
		return Refs{}, false, err
	}
	// show-ref exits 1 when there are no refs at all, as in a new repository.
	out, err := offlinegit.Script(run, offlinegit.Job{ReadOnly: []string{project}, Dir: project},
		`g show-ref --head --heads --tags; s=$?; [ "$s" -le 1 ]`)
	if err != nil {
		return Refs{}, false, fmt.Errorf("git show-ref: %w", err)
	}
	refs, err = parseRefs(out)
	return refs, err == nil, err
}

func parseRefs(out string) (Refs, error) {
	refs := Refs{Tips: map[string]string{}}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		id, name, ok := strings.Cut(line, " ")
		if !ok || !objectID.MatchString(id) {
			return Refs{}, fmt.Errorf("git show-ref: unexpected line %q", clean(line))
		}
		if name == "HEAD" {
			refs.Head = id
		} else {
			refs.Tips[name] = id
		}
	}
	return refs, nil
}

// Equal reports whether nothing moved.
func (r Refs) Equal(o Refs) bool {
	return r.Head == o.Head && maps.Equal(r.Tips, o.Tips)
}

// RefChange is a branch or tag that was created, moved or deleted.
type RefChange struct {
	// Name is the full ref name.
	Name string
	Kind Kind
}

// RefChanges returns the refs that differ between before and after, sorted
// by name.
func RefChanges(before, after Refs) []RefChange {
	var out []RefChange
	for _, c := range DiffDigests(before.Tips, after.Tips) {
		out = append(out, RefChange{c.Path, c.Kind})
	}
	return out
}

// File is one path a commit changed.
type File struct {
	Path string
	// Status is git's raw status letter: A, D, M or T (type changed).
	Status byte
	// Emptied is set when a modified file's new content is empty.
	Emptied bool
}

// Commit is one commit made while a command ran.
type Commit struct {
	ID, Subject string
	Files       []File
}

// History is what changed in a repository between two Refs.
type History struct {
	// Commits are reachable from a ref or HEAD now but weren't from any
	// before, newest first. Merge commits list no files.
	Commits []Commit
	// Rewritten is set when HEAD's previous commit is no longer reachable
	// from HEAD: after a reset, rebase, amend or branch switch.
	Rewritten bool
}

// The empty blob, in SHA-1 and SHA-256 repositories.
var emptyBlobs = []string{
	"e69de29bb2d1d6434b8b29ae775ad8c2e48c5391",
	"473a0f4c3be8a93681a267e3b1e9a7dcda1185436fe141f7749120a303721813",
}

// historyScript prints whether HEAD's old commit ($1) is still an ancestor
// of its new one ($2), then runs git log with the remaining arguments.
const historyScript = `old=$1 new=$2; shift 2
if [ -z "$old" ] || { [ -n "$new" ] && g merge-base --is-ancestor "$old" "$new"; }; then
  echo kept
else
  echo rewritten
fi
g log "$@"`

// readHistory returns the commits made between before and after, on any
// branch or tag or a detached HEAD, with the files each one changed.
func readHistory(project string, run offlinegit.Runner, before, after Refs) (History, error) {
	if before.Equal(after) {
		return History{}, nil
	}
	positive := tipIDs(after)
	if len(positive) == 0 {
		// Nothing is reachable now, so HEAD's old commit isn't either.
		return History{Rewritten: before.Head != ""}, nil
	}
	args := []string{before.Head, after.Head,
		"--no-textconv", "--no-ext-diff", "--no-renames", "--no-abbrev", "--raw", "-z",
		"--format=%x01%H%x00%s"}
	args = append(args, positive...)
	if negative := tipIDs(before); len(negative) > 0 {
		args = append(append(args, "--not"), negative...)
	}
	out, err := offlinegit.Script(run, offlinegit.Job{ReadOnly: []string{project}, Dir: project}, historyScript, args...)
	if err != nil {
		return History{}, fmt.Errorf("git log: %w", err)
	}
	first, rest, _ := strings.Cut(out, "\n")
	commits, err := parseLog(rest)
	if err != nil {
		return History{}, err
	}
	return History{Commits: commits, Rewritten: first == "rewritten"}, nil
}

// tipIDs returns the distinct objects r's refs and HEAD point at, sorted.
func tipIDs(r Refs) []string {
	ids := slices.Collect(maps.Values(r.Tips))
	if r.Head != "" {
		ids = append(ids, r.Head)
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

// parseLog reads git log output in the format readHistory asks for: per
// commit, \x01, the ID, NUL, the subject, NUL, then raw entries of a status
// line and a path, each NUL-terminated. It reads front to back, so a
// subject can't pass for a commit or an entry.
func parseLog(out string) ([]Commit, error) {
	bad := errors.New("git log: unexpected output")
	field := func() (string, bool) {
		f, rest, ok := strings.Cut(out, "\x00")
		out = rest
		return f, ok
	}
	var commits []Commit
	for out != "" {
		if out[0] != '\x01' {
			return nil, bad
		}
		out = out[1:]
		id, ok := field()
		if !ok || !objectID.MatchString(id) {
			return nil, bad
		}
		subject, ok := field()
		if !ok {
			return nil, bad
		}
		c := Commit{ID: id, Subject: subject}
		out = strings.TrimPrefix(out, "\n")
		for strings.HasPrefix(out, ":") {
			meta, ok := field()
			if !ok {
				return nil, bad
			}
			path, ok := field()
			if !ok {
				return nil, bad
			}
			// :<old mode> <new mode> <old id> <new id> <status>
			parts := strings.Fields(meta)
			if len(parts) != 5 || parts[4] == "" {
				return nil, bad
			}
			f := File{Path: path, Status: parts[4][0]}
			f.Emptied = f.Status == 'M' && slices.Contains(emptyBlobs, parts[3])
			c.Files = append(c.Files, f)
		}
		commits = append(commits, c)
	}
	return commits, nil
}

// Reason is why a path in a commit calls for attention.
type Reason string

const (
	// Placeholder: an empty file kota mounted where the project had none.
	Placeholder Reason = "placeholder"
	// Masked: a secret kota showed empty in the sandbox.
	Masked Reason = "masked"
	// GloballyIgnored: a file the host's global git excludes ignore.
	GloballyIgnored Reason = "ignored"
	// HiddenWatched: a watched file whose change the working tree doesn't
	// show, so the working tree report missed it.
	HiddenWatched Reason = "watched"
)

// commitPaths says which paths in commits call for a warning.
type commitPaths struct {
	// Placeholders and Masked are relative paths with forward slashes; a
	// trailing slash marks a directory, which covers everything inside.
	Placeholders, Masked []string
	// Ignored reports whether the host's global excludes ignore a path.
	// Nil when there are none.
	Ignored func(rel string) bool
	// Watch are extra watched files, as for snapshotWith.
	Watch []string
	// Reported are the paths the working tree report already listed.
	Reported []Change
}

// PathChange is one commit's change to a path.
type PathChange struct {
	Commit string
	// Kind is added, deleted, modified, emptied or changed type.
	Kind string
}

// CommitWarning is a path that one or more commits changed, and why that
// needs the user's attention.
type CommitWarning struct {
	Path    string
	Reason  Reason
	Changes []PathChange
}

// checkCommits returns a warning for each path in commits that touches a
// placeholder or masked path, adds a globally ignored file, or changes a
// watched file the working tree report didn't list. Warnings are sorted by
// reason, then path, and list the commits oldest first.
func checkCommits(commits []Commit, p commitPaths) []CommitWarning {
	reported := map[string]bool{}
	for _, c := range p.Reported {
		reported[c.Path] = true
	}
	byPath := map[string]*CommitWarning{}
	for _, c := range slices.Backward(commits) {
		for _, f := range c.Files {
			reason, ok := p.reason(f, reported)
			if !ok {
				continue
			}
			w := byPath[f.Path]
			if w == nil {
				w = &CommitWarning{Path: f.Path, Reason: reason}
				byPath[f.Path] = w
			}
			w.Changes = append(w.Changes, PathChange{Commit: c.ID, Kind: fileKind(f)})
		}
	}
	order := []Reason{Placeholder, Masked, GloballyIgnored, HiddenWatched}
	out := make([]CommitWarning, 0, len(byPath))
	for _, w := range byPath {
		out = append(out, *w)
	}
	slices.SortFunc(out, func(a, b CommitWarning) int {
		if d := slices.Index(order, a.Reason) - slices.Index(order, b.Reason); d != 0 {
			return d
		}
		return strings.Compare(a.Path, b.Path)
	})
	return out
}

func (p commitPaths) reason(f File, reported map[string]bool) (Reason, bool) {
	switch {
	case covers(p.Placeholders, f.Path):
		return Placeholder, true
	case covers(p.Masked, f.Path):
		return Masked, true
	case f.Status == 'A' && p.Ignored != nil && p.Ignored(f.Path):
		return GloballyIgnored, true
	case !reported[f.Path] && IsWatched(f.Path, p.Watch):
		return HiddenWatched, true
	}
	return "", false
}

// covers reports whether rel is one of paths, or inside one ending in "/".
func covers(paths []string, rel string) bool {
	return slices.ContainsFunc(paths, func(p string) bool {
		if strings.HasSuffix(p, "/") {
			return rel == strings.TrimSuffix(p, "/") || strings.HasPrefix(rel, p)
		}
		return rel == p
	})
}

func fileKind(f File) string {
	switch {
	case f.Emptied:
		return "emptied"
	case f.Status == 'A':
		return "added"
	case f.Status == 'D':
		return "deleted"
	case f.Status == 'T':
		return "changed type"
	}
	return "modified"
}

// IsWatched reports whether the working tree report watches the file at
// rel: it matches Watched, or is one of extra. Like the report, it doesn't
// look inside the skipped directories unless they are inside a watched one.
// A path matching a watched directory counts too, since it may be a symlink.
func IsWatched(rel string, extra []string) bool {
	if slices.Contains(extra, rel) {
		return true
	}
	ps := parse(Watched)
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		if slices.Contains(skipDirs, parts[i-1]) {
			return false
		}
		if matches(ps, strings.Join(parts[:i], "/"), true) {
			return true
		}
	}
	return matches(ps, rel, false) || matches(ps, rel, true)
}

// maxCommits is how many commits the report lists.
const maxCommits = 20

// historyLines formats the ref changes, a rewritten HEAD and the commits
// for the user, one line each, without indentation. It returns nothing when
// nothing changed.
func historyLines(before, after Refs, h History) []string {
	var out []string
	for _, c := range RefChanges(before, after) {
		out = append(out, refLine(c))
	}
	if h.Rewritten {
		old := short(before.Head)
		out = append(out, fmt.Sprintf("HEAD no longer contains its previous commit %s (reset, rebase, amend or branch switch). To get it back: git reset --hard %s (or see git reflog)", old, old))
	}
	if n := len(h.Commits); n > 0 {
		out = append(out, fmt.Sprintf("%d new commit(s):", n))
		for i, c := range h.Commits {
			if i == maxCommits {
				out = append(out, fmt.Sprintf("  ... and %d more", n-maxCommits))
				break
			}
			out = append(out, "  "+short(c.ID)+"  "+clip(clean(c.Subject), 72))
		}
	}
	return out
}

func refLine(c RefChange) string {
	kind, name := "ref", c.Name
	if n, ok := strings.CutPrefix(name, "refs/heads/"); ok {
		kind, name = "branch", n
	} else if n, ok := strings.CutPrefix(name, "refs/tags/"); ok {
		kind, name = "tag", n
	}
	name = clip(clean(name), 100)
	switch c.Kind {
	case Added:
		return fmt.Sprintf("new %s %s", kind, name)
	case Deleted:
		return fmt.Sprintf("%s %s deleted", kind, name)
	}
	return fmt.Sprintf("%s %s moved", kind, name)
}

var reasonText = map[Reason]string{
	Placeholder:     "held empty by kota, not in the project",
	Masked:          "masked secret, shown empty in the sandbox",
	GloballyIgnored: "ignored by your global git excludes",
	HiddenWatched:   "can run code on the host, change not in the working tree",
}

// warningLines formats warnings, one path per line, followed by how to put
// back placeholder and masked paths. It returns nothing for no warnings.
func warningLines(warnings []CommitWarning) []string {
	var out, remove, restore []string
	hidden := false
	for _, w := range warnings {
		var changes []string
		for _, c := range w.Changes {
			changes = append(changes, c.Kind+" in "+short(c.Commit))
		}
		out = append(out, fmt.Sprintf("%s  %s: %s", display(w.Path), reasonText[w.Reason], strings.Join(changes, ", ")))
		hidden = hidden || w.Reason == HiddenWatched
		if w.Reason != Placeholder && w.Reason != Masked {
			continue
		}
		if slices.ContainsFunc(w.Changes, func(c PathChange) bool { return c.Kind == "added" }) {
			remove = append(remove, w.Path)
		} else {
			restore = append(restore, w.Path)
		}
	}
	if hidden {
		out = append(out, "A change to a file that runs code, made only in a commit, takes effect when the commit is checked out, merged or pushed (CI). Review it with git show.")
	}
	if len(remove)+len(restore) > 0 {
		out = append(out, "Masked and placeholder paths keep their real state on disk; only the commits differ. To fix them in a new commit, on the branch that has these commits:")
		if len(remove) > 0 {
			out = append(out, "  git rm --cached --"+shellWords(remove))
		}
		if len(restore) > 0 {
			out = append(out, "  git add --"+shellWords(restore))
		}
		out = append(out, "  git commit")
	}
	return out
}

func short(id string) string { return id[:min(len(id), 7)] }

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-3]) + "..."
	}
	return s
}

// clean drops control characters, so text from the repository can't move
// the cursor or rewrite earlier lines of the terminal.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 {
			return -1
		}
		return r
	}, s)
}

// display shows a path as is, or quoted when it holds control characters
// or spaces.
func display(p string) string {
	if clean(p) != p || strings.ContainsAny(p, " \t") {
		return fmt.Sprintf("%q", p)
	}
	return p
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_./@%+=:,-]+$`)

// shellWords quotes each path for a POSIX shell, after a space. A path with
// control characters is quoted with Go syntax instead; the user has to
// adapt it, but it can't do anything to the terminal.
func shellWords(paths []string) string {
	var b strings.Builder
	for _, p := range paths {
		switch {
		case plainWord.MatchString(p):
			b.WriteString(" " + p)
		case clean(p) != p:
			b.WriteString(" " + fmt.Sprintf("%q", p))
		default:
			b.WriteString(" '" + strings.ReplaceAll(p, "'", `'\''`) + "'")
		}
	}
	return b.String()
}

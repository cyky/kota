// Package nohistory makes the No-history copy a --no-history Session runs
// on: a clone of the commit a project's HEAD points at, with no older
// history, remotes or config.
//
// git never runs on the host against the project here. An agent may have
// written its config in an earlier Session, and git clone runs
// upload-pack against it, so the inspection and the clone run in offline
// containers, the project mounted read-only.
package nohistory

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"kota/internal/changereport"
	"kota/internal/deps"
	"kota/internal/interrupt"
	"kota/internal/mask"
	"kota/internal/offlinegit"
	"kota/internal/projectimage"
	"kota/internal/projectskills"
)

// ConfigFiles are kota's own files in a project. A Session on the copy
// takes them from the project, not from the commit: an untracked
// .kotaignore that masks a tracked secret must mask it in the copy too.
var ConfigFiles = []string{mask.IgnoreFile, deps.ConfigFile, projectimage.File}

// The copy also takes the Project skills from the project (Clone), when
// the Session loads them, so it sees the skills the user sees. Read-only,
// they are left out of git like ConfigFiles; writable, the agent's changes
// to them are its own, and come back like any other, while the user's
// uncommitted skills aren't.

// Copied is what a No-history copy took from the project besides its
// commit, for Return.
type Copied struct {
	// Skills is how the Session treats the Project skills.
	Skills projectskills.Mode
	// skills are the digests of the copy's Project skills as the Session got
	// them, with Writable: a skill that differs at the end is the agent's
	// work, the others are the user's.
	skills map[string]string
}

// taken reports whether the copy takes every path of a git status
// --porcelain line from the project: git status in the project need not
// list it.
func taken(line string, skills projectskills.Mode) bool {
	return allPaths(line, func(p string) bool {
		return slices.Contains(ConfigFiles, p) || skills != projectskills.Off && inSkills(p)
	})
}

// own reports whether every path of a git status --porcelain line in the
// copy is kota's, not the agent's: a change there isn't the Session's work.
// skill reports whether a path in the Project skills is.
func own(line string, skill func(p string) bool) bool {
	return allPaths(line, func(p string) bool {
		return slices.Contains(ConfigFiles, p) || inSkills(p) && skill(p)
	})
}

func inSkills(p string) bool {
	return strings.TrimSuffix(p, "/") == projectskills.Dir || strings.HasPrefix(p, projectskills.Dir+"/")
}

// allPaths reports whether f holds for each path of a git status
// --porcelain line: both of a rename or copy ("old -> new"), unquoted as
// git quotes unusual names.
func allPaths(line string, f func(p string) bool) bool {
	paths := strings.Split(line[min(3, len(line)):], " -> ")
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, `"`) {
			u, err := strconv.Unquote(p)
			if err != nil {
				return false
			}
			p = u
		}
		if !f(p) {
			return false
		}
	}
	return true
}

// inspectScript fails on a repository without commits, then lists the
// tree at HEAD, a marker no tree entry can look like, and the paths stored
// in Git LFS.
const inspectScript = `g rev-parse -q --verify 'HEAD^{commit}' >/dev/null || { echo 'the repository has no commits yet' >&2; exit 1; }
g ls-tree -r -z --full-tree HEAD
printf 'lfs\0'
g ls-files -z -- ':(attr:filter=lfs)'`

// Missing is what the copy leaves out of the project, for the user to
// confirm before the Session.
type Missing struct {
	// Changes are git status --porcelain lines, kota's own files left out:
	// the copy takes those from the project.
	Changes []string
	// Submodules are submodule paths. The copy has empty directories there.
	Submodules []string
	// LFS are paths stored in Git LFS. The copy has their pointer files.
	LFS []string
}

// Empty reports whether the copy leaves nothing out.
func (m Missing) Empty() bool {
	return len(m.Changes) == 0 && len(m.Submodules) == 0 && len(m.LFS) == 0
}

// Inspect checks that project is the top of a git repository with at least
// one commit, and returns what a copy of it would leave out. skills is how
// the Session treats the Project skills.
func Inspect(project string, run offlinegit.Runner, skills projectskills.Mode) (Missing, error) {
	repo, err := repoOf(project)
	if err != nil {
		return Missing{}, err
	}
	out, err := offlinegit.Script(run, offlinegit.Job{ReadOnly: repo.Mounts, Dir: project}, inspectScript)
	if err != nil {
		return Missing{}, fmt.Errorf("inspect the repository: %w", err)
	}
	m, err := parseInspect(out)
	if err != nil {
		return Missing{}, err
	}
	status, err := offlinegit.ReadStatus(project, run)
	if err != nil {
		return Missing{}, err
	}
	for _, line := range status.Changes {
		if !taken(line, skills) {
			m.Changes = append(m.Changes, line)
		}
	}
	return m, nil
}

// repoOf finds project's repository, which must have its .git in project
// itself: the copy is a clone of the whole repository.
func repoOf(project string) (offlinegit.Repo, error) {
	repo, ok, err := offlinegit.FindRepo(project)
	if err != nil {
		return offlinegit.Repo{}, err
	}
	if !ok {
		return offlinegit.Repo{}, fmt.Errorf("--no-history needs a git repository, and %s isn't in one", project)
	}
	if repo.Top != project {
		return offlinegit.Repo{}, fmt.Errorf("%s is inside a larger git repository; --no-history copies whole repositories, so run it at the repository's top", project)
	}
	return repo, nil
}

func parseInspect(out string) (Missing, error) {
	// Tree entries start with their mode, so the marker is the first entry
	// that starts with "lfs". A path ending in "lfs" follows a tab.
	tree, lfs, ok := strings.Cut("\x00"+out, "\x00lfs\x00")
	if !ok {
		return Missing{}, errors.New("inspect the repository: unexpected output")
	}
	var m Missing
	for _, entry := range strings.Split(tree, "\x00") {
		// <mode> SP <type> SP <object> TAB <path>
		info, path, ok := strings.Cut(entry, "\t")
		if ok && strings.HasPrefix(info, "160000 commit ") {
			m.Submodules = append(m.Submodules, path)
		}
	}
	for _, path := range strings.Split(lfs, "\x00") {
		if path != "" {
			m.LFS = append(m.LFS, path)
		}
	}
	return m, nil
}

// maxListed is how many paths of each kind Confirm shows.
const maxListed = 10

// Confirm describes what the copy leaves out on out and reads one line
// from in. Only "y" (or "Y") continues; anything else, including end of
// input, declines.
func Confirm(in io.Reader, out io.Writer, m Missing) bool {
	fmt.Fprintln(out, "--no-history runs the session on a copy of the last commit. These are not in the copy:")
	list := func(what string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(out, "  %s:\n", what)
		for i, item := range items {
			if i == maxListed {
				fmt.Fprintf(out, "    ... and %d more\n", len(items)-maxListed)
				break
			}
			fmt.Fprintln(out, "    "+item)
		}
	}
	list("uncommitted changes", m.Changes)
	list("submodules (empty in the copy)", m.Submodules)
	list("Git LFS files (pointer files in the copy)", m.LFS)
	fmt.Fprint(out, "Type y to start without them: ")
	answer := strings.TrimSpace(interrupt.ReadLine(in))
	return answer == "y" || answer == "Y"
}

// cloneScript clones $1 into $2 with only its HEAD commit, then removes
// the remote and the reflog, which name the project's path, and keeps
// kota's own files ($3...) out of git add when they are untracked.
const cloneScript = `set -e
src=$1 dest=$2; shift 2
g -c protocol.file.allow=always clone -q --no-local --depth 1 --single-branch --no-tags --template= -- "$src" "$dest"
cd "$dest"
g remote remove origin
g reflog expire --expire=now --all
mkdir -p .git/info
for f; do printf '/%s\n' "$f"; done > .git/info/exclude`

// skillsScript replaces the Project skills folder $2 in the copy, the
// current directory, with the project's $1/$2, if it has one. It runs in
// the clone's container, so the Docker VM sees the folder as written: one
// replaced from the host could be mounted as the VM's stale view of the
// old one. A link at .agents or the folder, from the commit, is removed as
// it is, so nothing is removed or written through it; cp -a copies the
// project's links as links. With $3 read-only, git is told to ignore the
// tracked skills' working-tree state, so the user's uncommitted skill
// edits and deletions can't be staged or committed in the copy.
const skillsScript = `set -e
src=$1/$2 dir=$2 parent=${2%/*}
for p in "$parent" "$dir"; do
	if [ -L "$p" ] || { [ -e "$p" ] && [ ! -d "$p" ]; }; then rm -f "$p"; fi
done
rm -rf "$dir"
if [ -d "$src" ]; then mkdir -p "$parent" && cp -a "$src" "$dir"; fi
if [ "$3" = read-only ]; then g ls-files -z -- "$dir" | g update-index -z --skip-worktree --stdin; fi`

// Clone clones the commit project's HEAD points at into dest, which must
// not exist yet. dest's parent is the only writable mount. On an error,
// the caller removes dest. skills is how the Session treats the Project
// skills: unless Off, the copy has the project's, uncommitted ones
// included, and read-only, git in the copy leaves them out of commits.
func Clone(project, dest string, run offlinegit.Runner, skills projectskills.Mode) (Copied, error) {
	copied := Copied{Skills: skills}
	repo, err := repoOf(project)
	if err != nil {
		return copied, err
	}
	if skills != projectskills.Off {
		if err := projectskills.CheckPath(project); err != nil {
			return copied, err
		}
	}
	parent := filepath.Dir(dest)
	job := offlinegit.Job{ReadOnly: repo.Mounts, Writable: parent, Dir: parent}
	excluded := slices.Clone(ConfigFiles)
	if skills == projectskills.ReadOnly {
		excluded = append(excluded, projectskills.Dir+"/")
	}
	if _, err := offlinegit.Script(run, job, cloneScript, append([]string{project, dest}, excluded...)...); err != nil {
		return copied, fmt.Errorf("copy the project: %w", err)
	}
	if skills == projectskills.Off {
		return copied, nil
	}
	job.Dir = dest
	mode := "writable"
	if skills == projectskills.ReadOnly {
		mode = "read-only"
	}
	if _, err := offlinegit.Script(run, job, skillsScript, project, projectskills.Dir, mode); err != nil {
		return copied, fmt.Errorf("copy the project's %s: %w", projectskills.Dir, err)
	}
	if skills == projectskills.Writable {
		files, err := changereport.SnapshotMatching(dest, []string{projectskills.Dir + "/"})
		if err != nil {
			return copied, fmt.Errorf("record the copy's %s: %w", projectskills.Dir, err)
		}
		copied.skills = files.Digests()
	}
	return copied, nil
}

// TakeConfig replaces kota's own files in the copy with the project's. A
// file the project doesn't have is removed from the copy.
func TakeConfig(project, copy string) error {
	for _, name := range ConfigFiles {
		dst := filepath.Join(copy, name)
		// Remove first: the commit may hold a symlink here, which writing
		// would follow.
		if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("replace %s in the copy: %w", name, err)
		}
		src := filepath.Join(project, name)
		info, err := os.Lstat(src)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to copy %s: it is a %s, not a regular file", src, info.Mode().Type())
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return fmt.Errorf("copy %s: %w", name, err)
		}
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("copy %s: %w", name, err)
		}
	}
	return nil
}

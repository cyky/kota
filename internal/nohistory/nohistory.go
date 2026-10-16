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
	"strings"

	"kota/internal/approval"
	"kota/internal/deps"
	"kota/internal/mask"
	"kota/internal/projectimage"
)

// ConfigFiles are kota's own files in a project. A Session on the copy
// takes them from the project, not from the commit: an untracked
// .kotaignore that masks a tracked secret must mask it in the copy too.
var ConfigFiles = []string{mask.IgnoreFile, deps.ConfigFile, projectimage.File}

// WritableRunner is approval.Runner with writable also mounted at its own
// host path, and writable there.
type WritableRunner func(mounts []string, writable, dir string, cmd ...string) (string, error)

// gitFunc runs git hardened as the change report does: no fsmonitor, no index
// writes, no ownership check.
const gitFunc = `g() { git -c 'safe.directory=*' -c core.fsmonitor=false --no-optional-locks "$@"; }
`

// inspectScript fails on a repository without commits, then lists the
// tree at HEAD, a marker no tree entry can look like, and the paths stored
// in Git LFS.
const inspectScript = gitFunc + `g rev-parse -q --verify 'HEAD^{commit}' >/dev/null || { echo 'the repository has no commits yet' >&2; exit 1; }
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
// one commit, and returns what a copy of it would leave out.
func Inspect(project string, run approval.Runner) (Missing, error) {
	repo, err := repoOf(project)
	if err != nil {
		return Missing{}, err
	}
	out, err := run(repo.Mounts, project, "sh", "-c", inspectScript)
	if err != nil {
		return Missing{}, fmt.Errorf("inspect the repository: %w", err)
	}
	m, err := parseInspect(out)
	if err != nil {
		return Missing{}, err
	}
	status, err := approval.ReadStatus(project, run)
	if err != nil {
		return Missing{}, err
	}
	for _, line := range status.Changes {
		if !slices.Contains(ConfigFiles, strings.TrimSpace(line[min(3, len(line)):])) {
			m.Changes = append(m.Changes, line)
		}
	}
	return m, nil
}

// repoOf finds project's repository, which must have its .git in project
// itself: the copy is a clone of the whole repository.
func repoOf(project string) (approval.Repo, error) {
	repo, ok, err := approval.FindRepo(project)
	if err != nil {
		return approval.Repo{}, err
	}
	if !ok {
		return approval.Repo{}, fmt.Errorf("--no-history needs a git repository, and %s isn't in one", project)
	}
	if _, err := os.Lstat(filepath.Join(project, ".git")); errors.Is(err, fs.ErrNotExist) {
		return approval.Repo{}, fmt.Errorf("%s is inside a larger git repository; --no-history copies whole repositories, so run it at the repository's top", project)
	} else if err != nil {
		return approval.Repo{}, err
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
	answer := strings.TrimSpace(approval.ReadLine(in))
	return answer == "y" || answer == "Y"
}

// cloneScript clones $1 into $2 with only its HEAD commit, then removes
// the remote and the reflog, which name the project's path, and keeps
// kota's own files ($3...) out of git add when they are untracked.
const cloneScript = `set -e
g() { git -c 'safe.directory=*' -c core.fsmonitor=false -c protocol.file.allow=always "$@"; }
src=$1 dest=$2; shift 2
g clone -q --no-local --depth 1 --single-branch --no-tags --template= -- "$src" "$dest"
cd "$dest"
g remote remove origin
g reflog expire --expire=now --all
mkdir -p .git/info
for f; do printf '/%s\n' "$f"; done > .git/info/exclude`

// Clone clones the commit project's HEAD points at into dest, which must
// not exist yet. dest's parent is the only writable mount. On an error,
// the caller removes dest.
func Clone(project, dest string, run WritableRunner) error {
	repo, err := repoOf(project)
	if err != nil {
		return err
	}
	parent := filepath.Dir(dest)
	args := append([]string{"sh", "-c", cloneScript, "sh", project, dest}, ConfigFiles...)
	if _, err := run(repo.Mounts, parent, parent, args...); err != nil {
		return fmt.Errorf("copy the project: %w", err)
	}
	return nil
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

package offlinegit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Repo is the git repository a project belongs to.
type Repo struct {
	// Top is the directory holding .git: the project or one above it.
	Top string
	// Linked is set when .git is a file naming the git dir elsewhere, as in
	// a linked worktree or a submodule checkout.
	Linked bool
	// CommonDir is the git dir holding the objects, refs and config: .git
	// itself, or the common dir a linked .git leads to.
	CommonDir string
	// Mounts are the host paths git reads for the project's status: the
	// project, plus the .git that holds it and, when .git is a file, the git
	// dir and common dir it leads to, and in a subdirectory the .gitignore
	// files above it. None lies inside another.
	Mounts []string
}

// FindRepo finds the repository of project the way git discovers it, by
// looking for .git in project and each directory above it, without running
// git. ok is false when there is none.
func FindRepo(project string) (repo Repo, ok bool, err error) {
	for dir := project; ; dir = filepath.Dir(dir) {
		dotGit := filepath.Join(dir, ".git")
		info, err := os.Lstat(dotGit)
		switch {
		case err == nil:
			return repoAt(project, dir, info)
		case !errors.Is(err, fs.ErrNotExist):
			return Repo{}, false, err
		case filepath.Dir(dir) == dir:
			return Repo{}, false, nil
		}
	}
}

func repoAt(project, top string, info fs.FileInfo) (Repo, bool, error) {
	dotGit := filepath.Join(top, ".git")
	repo := Repo{Top: top, CommonDir: dotGit}
	paths := []string{project, dotGit}
	switch {
	case info.IsDir():
	case info.Mode().IsRegular():
		// .git names the git dir, whose commondir names the directory
		// holding config and objects. A submodule's git dir has none.
		repo.Linked = true
		gitDir, err := pointer(dotGit, "gitdir: ")
		if err != nil {
			return Repo{}, false, err
		}
		paths = append(paths, gitDir)
		repo.CommonDir = gitDir
		common, err := pointer(filepath.Join(gitDir, "commondir"), "")
		if err == nil {
			paths = append(paths, common)
			repo.CommonDir = common
		} else if !errors.Is(err, fs.ErrNotExist) {
			return Repo{}, false, err
		}
	default:
		return Repo{}, false, fmt.Errorf("%s is a %s, not a directory or file", dotGit, info.Mode().Type())
	}
	ignores, err := ignoresAbove(project, top)
	if err != nil {
		return Repo{}, false, err
	}
	repo.Mounts = outermost(append(paths, ignores...))
	return repo, true, nil
}

// ignoresAbove returns the .gitignore files in top and each directory below
// it down to project's parent, which apply to project but aren't in it.
func ignoresAbove(project, top string) ([]string, error) {
	if project == top {
		return nil, nil
	}
	var files []string
	for dir := filepath.Dir(project); ; dir = filepath.Dir(dir) {
		file := filepath.Join(dir, ".gitignore")
		info, err := os.Lstat(file)
		switch {
		case err == nil && info.Mode().IsRegular():
			files = append(files, file)
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return nil, err
		}
		if dir == top {
			return files, nil
		}
	}
}

// pointer reads a file holding one path after prefix, as .git files and
// commondir do, and returns it made absolute against the file's directory.
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

// outermost drops each path that is inside another one.
func outermost(paths []string) []string {
	var out []string
	for _, p := range paths {
		inside := slices.ContainsFunc(paths, func(o string) bool {
			return o != p && strings.HasPrefix(p, strings.TrimSuffix(o, "/")+"/")
		})
		if !inside && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// Status is the project's working tree state.
type Status struct {
	// Git is false for a project that isn't a git repository.
	Git bool
	// Changes are `git status --porcelain` lines: modified, staged and
	// untracked paths.
	Changes []string
}

// ReadStatus returns the working tree state of project. Only changes inside
// project count: in a subdirectory of a larger repository, the rest is out
// of the agent's reach.
func ReadStatus(project string, run Runner) (Status, error) {
	repo, ok, err := FindRepo(project)
	if err != nil || !ok {
		return Status{}, err
	}
	// Named, not discovered: in a subdirectory, the project and the .git
	// above it are separate mounts, and discovery stops at the first.
	out, err := Git(run, Job{ReadOnly: repo.Mounts, Dir: project},
		"--git-dir="+filepath.Join(repo.Top, ".git"), "--work-tree="+repo.Top,
		"status", "--porcelain", "--untracked-files=normal", "--", ".")
	if err != nil {
		return Status{}, fmt.Errorf("git status: %w", err)
	}
	s := Status{Git: true}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line != "" {
			s.Changes = append(s.Changes, line)
		}
	}
	return s, nil
}

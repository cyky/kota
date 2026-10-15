// Package approval decides whether a session runs in full auto, and asks
// the user to confirm a full-auto session on a project with uncommitted
// changes, which the agent could overwrite without asking.
package approval

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Agents without a permission system of their own. The sandbox is their
// only boundary, so they always run in full auto.
var alwaysAuto = map[string]bool{"pi": true}

// FullAuto reports whether agent acts without asking: always for agents
// without permissions, otherwise only when auto was requested.
func FullAuto(agent string, auto bool) bool {
	return auto || alwaysAuto[agent]
}

// Status is the project's working tree state.
type Status struct {
	// Git is false for a project that isn't a git repository.
	Git bool
	// Changes are `git status --porcelain` lines: modified, staged and
	// untracked paths.
	Changes []string
}

// NeedsConfirmation reports whether starting a session must wait for the
// user to type y: a full-auto session in a git project with uncommitted
// changes. A non-git project has nothing to stash, so it is never asked.
func NeedsConfirmation(fullAuto bool, s Status) bool {
	return fullAuto && s.Git && len(s.Changes) > 0
}

// Runner runs cmd with working directory dir in an offline container that
// sees only mounts, each read-only at its own host path, and returns its
// stdout. A non-zero exit is an error.
type Runner func(mounts []string, dir string, cmd ...string) (string, error)

// ReadStatus returns the working tree state of project. Only changes inside
// project count: in a subdirectory of a larger repository, the rest is out
// of the agent's reach.
//
// git never runs on the host here: the agent may have written the
// repository's config (in a repository it created) or its .gitattributes,
// and git status runs the filters and fsmonitor they name. run keeps
// whatever they start offline and unable to write.
func ReadStatus(project string, run Runner) (Status, error) {
	repo, ok, err := FindRepo(project)
	if err != nil || !ok {
		return Status{}, err
	}
	out, err := run(repo.Mounts, project, "git", "-c", "safe.directory=*", "-c", "core.fsmonitor=false",
		"--no-optional-locks", "status", "--porcelain", "--untracked-files=normal", "--", ".")
	if err != nil {
		return Status{}, fmt.Errorf("git status: %w", err)
	}
	s := Status{Git: true}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line != "" {
			s.Changes = append(s.Changes, line)
		}
	}
	return s, nil
}

// Repo is the git repository a project belongs to.
type Repo struct {
	// Mounts are the host paths git reads for the project's status: the
	// project, plus the .git that holds it and, when .git is a file, the git
	// dir and common dir it leads to. None lies inside another.
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
			return repoAt(project, dotGit, info)
		case !errors.Is(err, fs.ErrNotExist):
			return Repo{}, false, err
		case filepath.Dir(dir) == dir:
			return Repo{}, false, nil
		}
	}
}

func repoAt(project, dotGit string, info fs.FileInfo) (Repo, bool, error) {
	paths := []string{project, dotGit}
	switch {
	case info.IsDir():
	case info.Mode().IsRegular():
		// A linked worktree or submodule checkout: .git names the git dir,
		// whose commondir names the directory holding config and objects.
		gitDir, err := pointer(dotGit, "gitdir: ")
		if err != nil {
			return Repo{}, false, err
		}
		paths = append(paths, gitDir)
		common, err := pointer(filepath.Join(gitDir, "commondir"), "")
		if err == nil {
			paths = append(paths, common)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return Repo{}, false, err
		}
	default:
		return Repo{}, false, fmt.Errorf("%s is a %s, not a directory or file", dotGit, info.Mode().Type())
	}
	return Repo{Mounts: outermost(paths)}, true, nil
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

// maxListed is how many changed paths the prompt shows.
const maxListed = 10

// Confirm describes the uncommitted changes on out and reads one line from
// in. Only "y" (or "Y") continues; anything else, including end of input,
// declines.
func Confirm(in io.Reader, out io.Writer, agent string, s Status) bool {
	fmt.Fprintf(out, "%s runs in full auto: it edits files and runs commands without asking.\n", agent)
	fmt.Fprintf(out, "The project has %d uncommitted change(s) it could overwrite:\n", len(s.Changes))
	for i, c := range s.Changes {
		if i == maxListed {
			fmt.Fprintf(out, "  ... and %d more\n", len(s.Changes)-maxListed)
			break
		}
		fmt.Fprintln(out, "  "+c)
	}
	fmt.Fprint(out, "Type y to start anyway: ")
	answer := strings.TrimSpace(readLine(in))
	return answer == "y" || answer == "Y"
}

// readLine reads up to a newline one byte at a time, so nothing after the
// answer is consumed: the rest of stdin belongs to the agent.
func readLine(in io.Reader) string {
	var line []byte
	b := make([]byte, 1)
	for {
		n, err := in.Read(b)
		if n == 1 {
			if b[0] == '\n' {
				break
			}
			line = append(line, b[0])
		}
		if err != nil {
			break
		}
	}
	return string(line)
}

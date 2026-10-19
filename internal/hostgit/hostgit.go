// Package hostgit runs git on the host for the few reads kota makes of a
// project before a Session: its tracked files, its config and the user's
// identity. Everything else runs git in offline containers (offlinegit).
//
// An earlier Session's agent can write most of the project, including
// .git/HEAD and files at the project root. git's repository discovery
// falls back to the project root as a bare repository when .git/HEAD is
// broken, so a planted root config could set core.fsmonitor, which git
// runs, and feed kota false values. So git here never discovers a
// repository: it is named with --git-dir, a discovered bare repository is
// refused, and fsmonitor is off. These -c settings apply to kota's own git
// only; no config is written.
package hostgit

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
)

// hardening keeps git from running a program the repository's config names
// for a read: fsmonitor is the one that reads and status-like commands run.
var hardening = []string{"-c", "safe.bareRepository=explicit", "-c", "core.fsmonitor=false"}

// Command is exec.Command for git with args, reading the repository whose
// top is project. When project has a .git (a directory, or a file pointing
// at a linked worktree's or submodule's git dir), git uses exactly that,
// and fails rather than fall back when it isn't a readable repository.
// Without one, git looks for a repository above project, which the agent
// can't write, and never takes the project itself as a bare one.
func Command(project string, args ...string) *exec.Cmd {
	var pre []string
	if _, err := os.Lstat(filepath.Join(project, ".git")); errors.Is(err, fs.ErrNotExist) {
		pre = []string{"-C", project}
	} else {
		pre = []string{"--git-dir=" + filepath.Join(project, ".git"), "--work-tree=" + project}
	}
	cmd := exec.Command("git", append(append(pre, hardening...), args...)...)
	// A relative path in git's output, such as a config origin, is
	// relative to the project.
	cmd.Dir = project
	return cmd
}

// ConfigFile is exec.Command for git config -f file with args: one config
// file read alone, which runs nothing. It runs from /, so no repository is
// discovered and none of its config is read.
func ConfigFile(file string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append(append(slices.Clone(hardening), "config", "-f", file), args...)...)
	cmd.Dir = "/"
	return cmd
}

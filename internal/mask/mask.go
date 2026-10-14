// Package mask plans the mounts laid over the project inside the sandbox.
//
// So far it protects files that the host later executes: git hooks, git
// config and direnv's .envrc are mounted read-only over themselves, and the
// git dir is mounted over itself so it can't be renamed away and replaced.
package mask

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"localllm/internal/sandbox"
)

// Mount is one path laid over the project.
type Mount struct {
	// Path is relative to the project, with forward slashes.
	Path string
	// Dir is true for directories.
	Dir bool
	// Pin mounts the path read-write over itself, which only stops it from
	// being renamed or removed. Otherwise the mount is read-only.
	Pin bool
	// Placeholder is set when Path doesn't exist: an empty file or directory
	// is mounted instead, so it can't be created. Docker leaves the empty
	// mount point behind on the host; Cleanup removes it.
	Placeholder bool
	// Seed is written to Path on the host by Prepare when Path is missing,
	// then mounted read-only over itself. Cleanup removes it if unchanged.
	// Used where an empty placeholder would break git.
	Seed string
}

// Layout is the set of mounts for a project.
type Layout struct {
	Mounts []Mount
	// Notes explain limits that apply to this project, e.g. that git won't
	// work inside the sandbox.
	Notes []string
}

// Plan inspects project and returns its mounts. It refuses projects where a
// protected path is a symlink, since a mount there can't hold.
func Plan(project string) (Layout, error) {
	p := &planner{project: project}
	if err := p.file(".envrc"); err != nil {
		return Layout{}, err
	}
	if err := p.git(); err != nil {
		return Layout{}, err
	}
	slices.SortStableFunc(p.plan.Mounts, func(a, b Mount) int { return strings.Compare(a.Path, b.Path) })
	return p.plan, nil
}

type planner struct {
	project string
	plan    Layout
}

func (p *planner) git() error {
	info, err := p.lstat(".git")
	switch {
	case errors.Is(err, fs.ErrNotExist):
		p.note("not a git repository: nothing to protect in .git; a repository the agent creates is not protected")
		return nil
	case err != nil:
		return err
	case info.Mode().IsRegular():
		// A linked worktree or submodule checkout: .git points at a git dir
		// outside the project, which the sandbox can't see. Keep the pointer
		// from being redirected to a git dir the agent writes.
		p.add(Mount{Path: ".git"})
		p.note(".git is a file (worktree or submodule): its git dir is outside the project, so git does not work inside the sandbox")
		return nil
	case !info.IsDir():
		return fmt.Errorf("refusing to protect .git: it is a %s", info.Mode().Type())
	}

	p.add(Mount{Path: ".git", Dir: true, Pin: true})
	if err := p.gitDir(".git"); err != nil {
		return err
	}
	if err := p.hooksPath(); err != nil {
		return err
	}
	if err := p.worktrees(); err != nil {
		return err
	}
	return p.modules()
}

// worktrees protects the commondir of each linked worktree's git dir, which
// host git reads when used in that worktree.
func (p *planner) worktrees() error {
	if err := p.noSymlinks(".git/worktrees"); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(p.project, ".git", "worktrees"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rel := ".git/worktrees/" + e.Name() + "/commondir"
		if _, err := p.lstat(rel); err == nil {
			if err := p.file(rel); err != nil {
				return err
			}
		}
	}
	return nil
}

// gitDir protects the files of the git dir at rel that decide what git runs.
func (p *planner) gitDir(rel string) error {
	if err := p.dir(rel+"/hooks"); err != nil {
		return err
	}
	if err := p.file(rel+"/config"); err != nil {
		return err
	}
	// commondir redirects git to another directory's config and hooks. Git
	// can't read an empty one, so a missing commondir is seeded with ".",
	// which points back at the git dir itself.
	if err := p.seeded(rel+"/commondir", ".\n"); err != nil {
		return err
	}
	if on, _ := gitConfig(filepath.Join(p.project, rel, "config"), "--bool", "extensions.worktreeConfig"); on == "true" {
		return p.file(rel+"/config.worktree")
	}
	return nil
}

// hooksPath protects a core.hooksPath that points into the project, as
// husky and similar tools set up. Every config level counts.
func (p *planner) hooksPath() error {
	cmd := exec.Command("git", "-C", p.project, "config", "--type=path", "--get", "core.hooksPath")
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return nil // not set
		}
		return fmt.Errorf("read core.hooksPath: %w", err)
	}
	hooks := strings.TrimSpace(string(out))
	if !filepath.IsAbs(hooks) {
		hooks = filepath.Join(p.project, hooks)
	}
	rel, err := filepath.Rel(p.project, filepath.Clean(hooks))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return nil // outside the project, out of the agent's reach
	}
	rel = filepath.ToSlash(rel)
	if rel != ".git/hooks" {
		p.note("core.hooksPath is " + rel + ": it is read-only, but hook scripts there may run other project files (husky runs .husky/*), which stay writable")
	}
	return p.dir(rel)
}

// modules protects every submodule git dir under rel/modules, nested ones
// included, and the .git pointer file in each submodule checkout.
func (p *planner) modules() error { return p.modulesIn(".git/modules") }

// modulesIn looks for git dirs under rel. Submodule names may contain
// slashes, so a directory that isn't a git dir is searched further.
func (p *planner) modulesIn(rel string) error {
	if err := p.noSymlinks(rel); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(p.project, filepath.FromSlash(rel)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := rel + "/" + e.Name()
		abs := filepath.Join(p.project, filepath.FromSlash(sub))
		if !exists(filepath.Join(abs, "HEAD")) || !exists(filepath.Join(abs, "config")) {
			if err := p.modulesIn(sub); err != nil {
				return err
			}
			continue
		}
		if err := p.gitDir(sub); err != nil {
			return err
		}
		if err := p.pointer(abs); err != nil {
			return err
		}
		if err := p.modulesIn(sub + "/modules"); err != nil {
			return err
		}
	}
	return nil
}

// pointer protects the .git file of the checkout that the submodule git dir
// at gitDir belongs to, found through its core.worktree.
func (p *planner) pointer(gitDir string) error {
	wt, err := gitConfig(filepath.Join(gitDir, "config"), "core.worktree")
	if err != nil || wt == "" {
		return err
	}
	if !filepath.IsAbs(wt) {
		wt = filepath.Join(gitDir, wt)
	}
	rel, err := filepath.Rel(p.project, filepath.Join(wt, ".git"))
	if err != nil || strings.HasPrefix(rel, "../") {
		return nil
	}
	rel = filepath.ToSlash(rel)
	if err := p.noSymlinks(rel); err != nil {
		return err
	}
	if info, err := p.lstat(rel); err == nil && info.Mode().IsRegular() {
		p.add(Mount{Path: rel})
	}
	return nil
}

func (p *planner) file(rel string) error { return p.protect(rel, false) }
func (p *planner) dir(rel string) error  { return p.protect(rel, true) }

// protect adds a read-only mount for rel, or a placeholder when it is
// missing, so it can't be created.
func (p *planner) protect(rel string, dir bool) error {
	if err := p.noSymlinks(rel); err != nil {
		return err
	}
	info, err := p.lstat(rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		p.add(Mount{Path: rel, Dir: dir, Placeholder: true})
		return nil
	case err != nil:
		return err
	case info.IsDir() != dir:
		return fmt.Errorf("refusing to protect %s: expected a %s", rel, kind(dir))
	}
	p.add(Mount{Path: rel, Dir: dir})
	return nil
}

// seeded protects the file rel, seeding it with content when missing.
func (p *planner) seeded(rel, content string) error {
	if err := p.noSymlinks(rel); err != nil {
		return err
	}
	if _, err := p.lstat(rel); errors.Is(err, fs.ErrNotExist) {
		p.add(Mount{Path: rel, Seed: content})
		return nil
	}
	return p.file(rel)
}

// noSymlinks refuses rel when it or any directory on the way to it is a
// symlink: the agent could replace the link with a real directory, and a
// mount through a link may land somewhere else. Components that don't exist
// end the check.
func (p *planner) noSymlinks(rel string) error {
	path := ""
	for _, part := range strings.Split(rel, "/") {
		path = strings.TrimPrefix(path+"/"+part, "/")
		info, err := p.lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("refusing to protect %s: %s is a symlink, which a read-only mount can't hold in place", rel, path)
		}
	}
	return nil
}

func (p *planner) add(m Mount) {
	if !slices.ContainsFunc(p.plan.Mounts, func(o Mount) bool { return o.Path == m.Path }) {
		p.plan.Mounts = append(p.plan.Mounts, m)
	}
}

func (p *planner) note(s string) { p.plan.Notes = append(p.plan.Notes, s) }

func (p *planner) lstat(rel string) (fs.FileInfo, error) {
	return os.Lstat(filepath.Join(p.project, filepath.FromSlash(rel)))
}

// Bind turns the layout into bind mounts for the sandbox. Placeholders are
// backed by an empty file or directory created in scratch.
func (l Layout) Bind(project, scratch string) (pinned, readOnly []sandbox.Mount, err error) {
	emptyFile := filepath.Join(scratch, "empty-file")
	emptyDir := filepath.Join(scratch, "empty-dir")
	if err := os.WriteFile(emptyFile, nil, 0o444); err != nil {
		return nil, nil, err
	}
	if err := os.Mkdir(emptyDir, 0o555); err != nil {
		return nil, nil, err
	}
	for _, m := range l.Mounts {
		mount := sandbox.Mount{
			Source: filepath.Join(project, filepath.FromSlash(m.Path)),
			Target: sandbox.Workspace + "/" + m.Path,
		}
		switch {
		case m.Placeholder && m.Dir:
			mount.Source = emptyDir
		case m.Placeholder:
			mount.Source = emptyFile
		}
		if m.Pin {
			pinned = append(pinned, mount)
		} else {
			readOnly = append(readOnly, mount)
		}
	}
	return pinned, readOnly, nil
}

// Prepare writes the seeded files to the host. Call Cleanup afterwards.
func (pl Layout) Prepare(project string) error {
	for _, m := range pl.Mounts {
		if m.Seed == "" {
			continue
		}
		path := filepath.Join(project, filepath.FromSlash(m.Path))
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return fmt.Errorf("seed %s: %w", m.Path, err)
		}
		_, err = f.WriteString(m.Seed)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("seed %s: %w", m.Path, err)
		}
	}
	return nil
}

// Cleanup removes what the session added to the host: seeded files that
// are unchanged, and the empty mount points Docker created for placeholder
// mounts. Anything with other content is kept.
func (pl Layout) Cleanup(project string) {
	// Deepest first, so a placeholder inside another is removed before it.
	for _, m := range slices.Backward(pl.Mounts) {
		path := filepath.Join(project, filepath.FromSlash(m.Path))
		info, err := os.Lstat(path)
		if err != nil {
			continue
		}
		switch {
		case m.Seed != "":
			if b, err := os.ReadFile(path); err == nil && string(b) == m.Seed {
				os.Remove(path)
			}
		case !m.Placeholder:
		case m.Dir && info.IsDir(), !m.Dir && info.Mode().IsRegular() && info.Size() == 0:
			os.Remove(path) // fails, and keeps it, for a non-empty directory
		}
	}
}

// gitConfig reads one key from a config file, without includes or other
// config levels. A missing key returns "".
func gitConfig(file string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"config", "-f", file, "--get"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return "", nil
		}
		return "", fmt.Errorf("git config -f %s: %w: %s", file, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func kind(dir bool) string {
	if dir {
		return "directory"
	}
	return "file"
}

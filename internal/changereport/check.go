package changereport

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"kota/internal/mask"
	"kota/internal/offlinegit"
)

// Options says what a Check reports on.
type Options struct {
	// Copy is set for a No-history copy: the history is reported as "in
	// the copy", and the working tree isn't, since the copy is discarded.
	Copy bool
	// ConfigHome is the user's config dir (XDG_CONFIG_HOME or ~/.config),
	// where git's global excludes file is by default (git/ignore). Empty
	// means there is no default file.
	ConfigHome string
}

// Check is a Change report in progress, for a command that runs project
// code in a container: Start before the containers start, Finish once
// they're gone but before the session's cleanup, then Report after the
// cleanup.
type Check struct {
	project string
	layout  mask.Layout
	run     offlinegit.Runner
	opts    Options
	// before is the working tree's watched files; nil for a copy.
	before Files
	// refs are the refs at Start. git is unset for a project without a
	// .git directory of its own, where nothing in the sandbox can commit.
	refs    Refs
	git     bool
	refsErr error
}

// Start records the watched files and the refs of project, mounted with
// layout. An unreadable working tree is an error; unreadable refs are
// reported as a warning by Report, and the command still runs. Call it
// after the session has written its placeholders, before any container
// starts.
func Start(project string, layout mask.Layout, run offlinegit.Runner, opts Options) (*Check, error) {
	c := &Check{project: project, layout: layout, run: run, opts: opts}
	if !opts.Copy {
		before, err := snapshotWith(project, layout.Watch)
		if err != nil {
			return nil, fmt.Errorf("snapshot watched files: %w", err)
		}
		// The empty file placeholders kota created were missing before,
		// and the cleanup removes them again.
		for _, m := range layout.Mounts {
			if m.Placeholder && !m.Dir {
				delete(before, m.Path)
			}
		}
		c.before = before
	}
	c.refs, c.git, c.refsErr = ReadRefs(project, run)
	return c, nil
}

// Result is what Finish read, for Report.
type Result struct {
	check *Check
	// after, history and err are the refs and commits after the command.
	after   Refs
	history History
	err     error
}

// Finish reads the refs and the commits made since Start. Call it once the
// containers are gone but before the session's cleanup: git needs the
// seeded files in .git, and Docker Desktop's file sharing can still show a
// removed file as present.
func (c *Check) Finish() *Result {
	r := &Result{check: c}
	if !c.git || c.refsErr != nil {
		return r
	}
	r.after, _, r.err = ReadRefs(c.project, c.run)
	if r.err == nil {
		r.history, r.err = readHistory(c.project, c.run, c.refs, r.after)
	}
	return r
}

// Refs returns the refs at Start and at Finish. ok is false when they
// couldn't be read, or the project has no .git directory of its own.
func (r *Result) Refs() (before, after Refs, ok bool) {
	c := r.check
	return c.refs, r.after, c.git && c.refsErr == nil && r.err == nil
}

// Report prints the Change report: the watched files the command added,
// modified or deleted in the working tree, then the branches, tags and
// commits it made, and the paths in those commits that need review. Call
// it after the session's cleanup has removed the placeholders.
func (r *Result) Report(w io.Writer) {
	c := r.check
	var reported []Change
	if !c.opts.Copy {
		reported = c.reportWorkingTree(w)
	}
	switch {
	case c.refsErr != nil:
		fmt.Fprintf(w, "WARNING: could not record git refs, so commits made while it runs won't be checked: %v\n", c.refsErr)
		return
	case !c.git:
		return
	case r.err != nil:
		fmt.Fprintf(w, "WARNING: could not check for new commits: %v\n", r.err)
		return
	}
	lines := historyLines(c.refs, r.after, r.history)
	if len(lines) == 0 {
		return
	}
	where := ""
	if c.opts.Copy {
		where = " in the copy"
	}
	fmt.Fprintf(w, "git history changed%s:\n", where)
	for _, line := range lines {
		fmt.Fprintln(w, "  "+line)
	}
	paths := pathsFor(c.layout, reported)
	ignored, err := globalExcludes(c.opts.ConfigHome)
	if err != nil {
		fmt.Fprintf(w, "WARNING: could not read your global git excludes, so commits aren't checked against them: %v\n", err)
	}
	paths.Ignored = ignored
	warnings := checkCommits(r.history.Commits, paths)
	if len(warnings) == 0 {
		return
	}
	fmt.Fprintf(w, "WARNING: %d path(s) in these commits need review before you push or merge them:\n", len(warnings))
	for _, line := range warningLines(warnings) {
		fmt.Fprintln(w, "  "+line)
	}
}

// reportWorkingTree warns about the watched files the command added,
// modified or deleted, says what changed in them or how to see it, and
// returns them.
func (c *Check) reportWorkingTree(w io.Writer) []Change {
	after, err := snapshotWith(c.project, c.layout.Watch)
	if err != nil {
		fmt.Fprintf(w, "WARNING: could not check for changed files that run things: %v\n", err)
		return nil
	}
	changes := diff(c.before, after)
	if len(changes) == 0 {
		return nil
	}
	fmt.Fprintf(w, "WARNING: the session changed %d file(s) that can run code on the host. Review them before building, installing or opening the project in an editor:\n", len(changes))
	for _, line := range reportLines(c.project, c.before, after, changes) {
		fmt.Fprintln(w, "  "+line)
	}
	return changes
}

// pathsFor are the paths the commit check looks for: the layout's
// placeholders and masks outside .git, and its extra watched files.
// reported are the watched files the working tree report already listed.
func pathsFor(layout mask.Layout, reported []Change) commitPaths {
	p := commitPaths{Watch: layout.Watch, Reported: reported}
	for _, m := range layout.Mounts {
		if mask.InGitDir(m.Path) {
			continue
		}
		rel := m.Path
		if m.Dir {
			rel += "/"
		}
		switch {
		case m.Placeholder:
			p.Placeholders = append(p.Placeholders, rel)
		case m.Mask:
			p.Masked = append(p.Masked, rel)
		}
	}
	return p
}

// maxGlobalExcludes caps the size of the global excludes file kota reads.
const maxGlobalExcludes = 1 << 20

// globalExcludes returns whether the host's global git excludes file
// ignores a path, or nil when there is no such file. Only the user's global
// config is read, from outside any repository: a repository config the
// agent wrote could name any file. The file is matched on the host and
// never reaches a container. configHome is as in Options.
func globalExcludes(configHome string) (func(rel string) bool, error) {
	// On the host: it reads the global config only, from /, so no
	// repository is discovered (see hostgit).
	cmd := exec.Command("git", "config", "--global", "--type=path", "--get", "core.excludesFile")
	cmd.Dir = "/"
	out, err := cmd.Output()
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		return nil, fmt.Errorf("git config --global core.excludesFile: %w", err)
	}
	file := strings.TrimSpace(string(out))
	if file == "" {
		if configHome == "" {
			return nil, nil
		}
		file = filepath.Join(configHome, "git", "ignore")
	}
	info, err := os.Stat(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, err
	case !info.Mode().IsRegular():
		return nil, fmt.Errorf("%s is not a regular file", file)
	case info.Size() > maxGlobalExcludes:
		return nil, fmt.Errorf("%s is larger than %d bytes", file, maxGlobalExcludes)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	ig, err := mask.ParseIgnore(data, file)
	if err != nil {
		return nil, err
	}
	return ig.Ignored, nil
}

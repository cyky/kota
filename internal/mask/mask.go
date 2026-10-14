// Package mask plans the mounts laid over the project inside the sandbox.
//
// Secrets matching the default rules or the project's .localllmignore are
// masked: hidden behind an empty read-only file or directory. Files the host
// later executes (git hooks, git config, direnv's .envrc) are mounted
// read-only over themselves, and the directories on the way to them are
// mounted over themselves so they can't be renamed away and replaced.
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

	"localllm/internal/deps"
	"localllm/internal/projectimage"
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
	// Mask hides the content: an empty read-only file or directory is
	// mounted over Path.
	Mask bool
}

// Layout is the set of mounts for a project.
type Layout struct {
	Mounts []Mount
	// Notes explain limits that apply to this project, e.g. that git won't
	// work inside the sandbox.
	Notes []string
	// Warnings call for the user's attention, e.g. a masked file tracked in
	// git.
	Warnings []string
}

// IgnoreFile is the per-project mask rules file, in gitignore syntax.
const IgnoreFile = ".localllmignore"

// DefaultRules are the mask rules every project gets, before IgnoreFile.
var DefaultRules = []string{
	".env",
	".env.*",
	"!.env.example",
	"!.env.sample",
	"!.env.template",
	"!.env.dist",
	"*.pem",
	"*.key",
	"*.p12",
	"*.pfx",
	"*.jks",
	"*.keystore",
	"id_rsa*",
	"id_dsa*",
	"id_ecdsa*",
	"id_ed25519*",
	"!*.pub",
	".npmrc",
	".pypirc",
	".netrc",
	".git-credentials",
	"*.tfstate",
	"*.tfstate.*",
	".aws/",
	".ssh/",
	".gnupg/",
}

// skipDirs are not searched for secrets: dependency and build output, which
// is large and not where secrets live. .git is never searched.
var skipDirs = []string{
	".git", "node_modules", ".venv", "venv", "__pycache__", "vendor",
	"target", "dist", "build", ".next", ".nuxt", ".gradle", ".terraform",
	".direnv", ".cache",
}

// Plan inspects project and returns its mounts: masks for paths matching
// defaults (normally DefaultRules) followed by the project's IgnoreFile, and
// the protections for files the host executes. It refuses projects where a
// protected path goes through a symlink, since a mount there can't hold.
func Plan(project string, defaults []string) (Layout, error) {
	project, err := filepath.EvalSymlinks(project)
	if err != nil {
		return Layout{}, err
	}
	p := &planner{project: project}
	if err := p.file(".envrc"); err != nil {
		return Layout{}, err
	}
	// The rules file decides what the next session hides, so the agent
	// mustn't edit it.
	if err := p.file(IgnoreFile); err != nil {
		return Layout{}, err
	}
	// The project config decides what prep runs with network access.
	if err := p.file(deps.ConfigFile); err != nil {
		return Layout{}, err
	}
	// The project's Dockerfile runs with network access at build time and
	// decides what every later session runs on.
	if err := p.file(projectimage.File); err != nil {
		return Layout{}, err
	}
	if err := p.git(); err != nil {
		return Layout{}, err
	}
	// Pin before masking: a mask may replace a protection or hide it inside
	// a masked directory, but the directories above must still hold.
	p.pinAncestors()
	rs, err := p.rules(defaults)
	if err != nil {
		return Layout{}, err
	}
	if err := p.masks(rs, ""); err != nil {
		return Layout{}, err
	}
	p.dropInsideMasks()
	slices.SortStableFunc(p.plan.Mounts, func(a, b Mount) int { return strings.Compare(a.Path, b.Path) })
	if err := p.warnTracked(); err != nil {
		return Layout{}, err
	}
	return p.plan, nil
}

func (p *planner) rules(defaults []string) (rules, error) {
	rs, err := parseRules(defaults, "default rules")
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(p.project, IgnoreFile))
	if errors.Is(err, fs.ErrNotExist) {
		return rs, nil
	} else if err != nil {
		return nil, err
	}
	own, err := parseRules(strings.Split(string(data), "\n"), IgnoreFile)
	if err != nil {
		return nil, err
	}
	return append(rs, own...), nil
}

// masks walks the directory rel (slash-separated, "" for the root) and
// masks every entry the rules exclude. Masked directories aren't entered.
func (p *planner) masks(rs rules, rel string) error {
	entries, err := os.ReadDir(filepath.Join(p.project, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	for _, e := range entries {
		path := strings.TrimPrefix(rel+"/"+e.Name(), "/")
		if e.IsDir() && slices.Contains(skipDirs, e.Name()) {
			continue
		}
		if rs.excluded(path, e.IsDir()) {
			if err := p.mask(path, e); err != nil {
				return err
			}
			continue
		}
		if e.IsDir() {
			if err := p.masks(rs, path); err != nil {
				return err
			}
		}
	}
	return nil
}

// mask hides path. A matching symlink hides what it points to, when that is
// inside the project; anything outside isn't mounted and so not reachable.
func (p *planner) mask(path string, e fs.DirEntry) error {
	if e.Type()&fs.ModeSymlink == 0 {
		p.add(Mount{Path: path, Dir: e.IsDir(), Mask: true})
		return nil
	}
	target, err := filepath.EvalSymlinks(filepath.Join(p.project, filepath.FromSlash(path)))
	if err != nil {
		return nil // dangling
	}
	rel, err := filepath.Rel(p.project, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return nil
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil
	}
	p.add(Mount{Path: filepath.ToSlash(rel), Dir: info.IsDir(), Mask: true})
	return nil
}

// dropInsideMasks removes mounts inside a masked directory: the mask
// already hides them and holds them read-only, and Docker couldn't create
// their mount points inside it.
func (p *planner) dropInsideMasks() {
	var dirs []string
	for _, m := range p.plan.Mounts {
		if m.Mask && m.Dir {
			dirs = append(dirs, m.Path+"/")
		}
	}
	p.plan.Mounts = slices.DeleteFunc(p.plan.Mounts, func(m Mount) bool {
		return slices.ContainsFunc(dirs, func(d string) bool { return strings.HasPrefix(m.Path, d) })
	})
}

// warnTracked warns about masked paths tracked in git: inside the sandbox
// they read as empty, so a commit there would record them emptied.
func (p *planner) warnTracked() error {
	if info, err := p.lstat(".git"); err != nil || !info.IsDir() {
		return nil
	}
	var masked []Mount
	for _, m := range p.plan.Mounts {
		if m.Mask {
			masked = append(masked, m)
		}
	}
	if len(masked) == 0 {
		return nil
	}
	out, err := exec.Command("git", "-C", p.project, "ls-files", "-z").Output()
	if err != nil {
		return fmt.Errorf("list tracked files: %w", err)
	}
	tracked := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	for _, m := range masked {
		hit := slices.ContainsFunc(tracked, func(f string) bool {
			return f == m.Path || m.Dir && strings.HasPrefix(f, m.Path+"/")
		})
		if hit {
			p.plan.Warnings = append(p.plan.Warnings, fmt.Sprintf(
				"masked %s is tracked in git: it reads as empty inside the sandbox, so a commit made there would record it emptied", m.Path))
		}
	}
	return nil
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
	return p.modules()
}

// gitDir protects the files of the git dir at rel that decide what git runs.
func (p *planner) gitDir(rel string) error {
	if err := p.dir(rel + "/hooks"); err != nil {
		return err
	}
	if err := p.file(rel + "/config"); err != nil {
		return err
	}
	// commondir redirects git to another directory's config and hooks. Git
	// can't read an empty one, so a missing commondir is seeded with ".",
	// which points back at the git dir itself.
	if err := p.seeded(rel+"/commondir", ".\n"); err != nil {
		return err
	}
	wtConfig, _ := gitConfig(filepath.Join(p.project, rel, "config"), "--bool", "extensions.worktreeConfig")
	if wtConfig == "true" {
		if err := p.file(rel + "/config.worktree"); err != nil {
			return err
		}
	}
	return p.linkedWorktrees(rel, wtConfig == "true")
}

// linkedWorktrees protects the per-worktree files of each linked worktree
// of the git dir at rel, which host git reads when used in that worktree.
func (p *planner) linkedWorktrees(rel string, wtConfig bool) error {
	dir := rel + "/worktrees"
	if err := p.noSymlinks(dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(p.project, filepath.FromSlash(dir)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		wt := dir + "/" + e.Name()
		if err := p.file(wt + "/commondir"); err != nil {
			return err
		}
		if wtConfig {
			if err := p.file(wt + "/config.worktree"); err != nil {
				return err
			}
		}
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
	rel, ok := p.inside(hooks)
	if !ok {
		return nil // outside the project, out of the agent's reach
	}
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
	rel, ok := p.inside(filepath.Join(wt, ".git"))
	if !ok {
		return nil
	}
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
		// Hold the first missing directory on the way, so nothing can be
		// created under it either.
		first := p.firstMissing(rel)
		p.add(Mount{Path: first, Dir: dir || first != rel, Placeholder: true})
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

// firstMissing returns the shortest prefix of rel that doesn't exist.
func (p *planner) firstMissing(rel string) string {
	parts := strings.Split(rel, "/")
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		if _, err := p.lstat(prefix); errors.Is(err, fs.ErrNotExist) {
			return prefix
		}
	}
	return rel
}

// pinAncestors pins every directory on the way to a protected path. A
// read-only mount only holds its own path: renaming a parent moves it
// aside, and a fresh parent can hold anything.
func (p *planner) pinAncestors() {
	for _, m := range slices.Clone(p.plan.Mounts) {
		if m.Pin || m.Mask {
			continue
		}
		parts := strings.Split(m.Path, "/")
		for i := 1; i < len(parts); i++ {
			p.add(Mount{Path: strings.Join(parts[:i], "/"), Dir: true, Pin: true})
		}
	}
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

// add adds m unless its path is already planned. A mask is stricter than
// any other mount, so it takes over a path's read-only or pinned mount.
func (p *planner) add(m Mount) {
	i := slices.IndexFunc(p.plan.Mounts, func(o Mount) bool { return o.Path == m.Path })
	switch {
	case i < 0:
		p.plan.Mounts = append(p.plan.Mounts, m)
	case m.Mask:
		o := &p.plan.Mounts[i]
		o.Mask, o.Pin = true, false
	}
}

func (p *planner) note(s string) { p.plan.Notes = append(p.plan.Notes, s) }

func (p *planner) lstat(rel string) (fs.FileInfo, error) {
	return os.Lstat(filepath.Join(p.project, filepath.FromSlash(rel)))
}

// Bind turns the layout into bind mounts for the sandbox. Placeholders and
// masks are backed by an empty file or directory created in scratch.
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
		case (m.Placeholder || m.Mask) && m.Dir:
			mount.Source = emptyDir
		case m.Placeholder || m.Mask:
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

// Targets returns the in-sandbox paths the probes check: pinned paths must
// be mount points, every other mount must reject writes, and masks must
// read as empty.
func (l Layout) Targets() (pinned, readOnly, masked []string) {
	for _, m := range l.Mounts {
		target := sandbox.Workspace + "/" + m.Path
		switch {
		case m.Pin:
			pinned = append(pinned, target)
		default:
			readOnly = append(readOnly, target)
		}
		if m.Mask {
			masked = append(masked, target)
		}
	}
	return pinned, readOnly, masked
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

// inside returns the project-relative form of the absolute path abs, if it
// lies inside the project. A path that is inside as written is returned as
// written, so its own symlinks get checked: the agent could replace them.
// Otherwise it counts when it resolves to inside, through links outside the
// project that the agent can't touch.
func (p *planner) inside(abs string) (string, bool) {
	for _, candidate := range []string{filepath.Clean(abs), resolveExisting(abs)} {
		rel, err := filepath.Rel(p.project, candidate)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, "../") {
			return filepath.ToSlash(rel), true
		}
	}
	return "", false
}

// resolveExisting resolves symlinks in the longest existing prefix of path,
// so it compares with the resolved project path even when the rest doesn't
// exist yet.
func resolveExisting(path string) string {
	path = filepath.Clean(path)
	var rest []string
	for {
		if r, err := filepath.EvalSymlinks(path); err == nil {
			return filepath.Join(append([]string{r}, rest...)...)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return filepath.Join(append([]string{path}, rest...)...)
		}
		rest = append([]string{filepath.Base(path)}, rest...)
		path = parent
	}
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

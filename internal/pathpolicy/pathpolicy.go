// Package pathpolicy decides which host paths may be mounted into the sandbox.
//
// Every path is resolved through symlinks before it is judged, so a link
// inside the allowed root can't smuggle in a path outside it.
package pathpolicy

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"localllm/internal/sandbox"
)

// Policy holds the fixed locations paths are judged against.
type Policy struct {
	// Home is the user's home directory. It is never mounted, nor is anything
	// containing it.
	Home string
	// AllowedRoot is the directory projects must live strictly inside.
	AllowedRoot string
	// Forbidden are paths that are never mounted, nor anything inside or
	// containing them. Missing paths are fine; they are matched literally.
	Forbidden []string
	// CaseInsensitive compares paths ignoring case, as on default macOS
	// volumes, where ~/.SSH opens ~/.ssh.
	CaseInsensitive bool
}

// Default returns the policy for a user with the given home directory.
// allowedRoot defaults to ~/Projects when empty.
func Default(home, allowedRoot string) Policy {
	if allowedRoot == "" {
		allowedRoot = filepath.Join(home, "Projects")
	}
	forbidden := []string{"/var/run/docker.sock", "/run/docker.sock"}
	// Credentials, and the directories Docker Desktop, Rancher Desktop and
	// Colima keep their sockets in.
	for _, dir := range []string{".ssh", ".config", ".gnupg", ".aws", ".kube", ".docker", ".rd", ".colima"} {
		forbidden = append(forbidden, filepath.Join(home, dir))
	}
	return Policy{
		Home:            home,
		AllowedRoot:     allowedRoot,
		Forbidden:       forbidden,
		CaseInsensitive: runtime.GOOS == "darwin",
	}
}

// Paths are the validated, symlink-free host paths for a session.
type Paths struct {
	// Project is mounted read-write at sandbox.Workspace.
	Project string
	// ReadOnly are the --ro paths with their targets under sandbox.ReadOnlyRoot.
	ReadOnly []sandbox.Mount
}

// Validate resolves project and the read-only extras and refuses any that
// would expose more than intended.
func Validate(project string, roExtras []string, p Policy) (Paths, error) {
	home, err := resolve(p.Home)
	if err != nil {
		return Paths{}, fmt.Errorf("home directory: %w", err)
	}
	root, err := resolve(p.AllowedRoot)
	if err != nil {
		return Paths{}, fmt.Errorf("allowed root %s: %w (set LOCALLLM_ALLOWED_ROOT to change it)", p.AllowedRoot, err)
	}
	forbidden := p.forbidden()
	c := checker{fold: p.CaseInsensitive}

	proj, err := resolve(project)
	if err != nil {
		return Paths{}, err
	}
	refuse := func(path, resolved, why string) error {
		if filepath.Clean(path) != resolved {
			path += " (resolves to " + resolved + ")"
		}
		return fmt.Errorf("refusing to mount %s: %s", path, why)
	}
	switch {
	case proj == "/":
		return Paths{}, refuse(project, proj, "it is the filesystem root")
	case c.equal(proj, home):
		return Paths{}, refuse(project, proj, "it is your home directory")
	case c.equal(proj, root):
		return Paths{}, refuse(project, proj, "it is the allowed root itself; pick a project inside it")
	case !c.within(proj, root):
		return Paths{}, refuse(project, proj, fmt.Sprintf("it is outside the allowed root %s (set LOCALLLM_ALLOWED_ROOT to change it)", root))
	}
	if f, ok := c.overlapsAny(proj, forbidden); ok {
		return Paths{}, refuse(project, proj, "it overlaps "+f+", which is never mounted")
	}
	if info, err := os.Stat(proj); err != nil {
		return Paths{}, err
	} else if !info.IsDir() {
		return Paths{}, fmt.Errorf("%s is not a directory", project)
	}

	out := Paths{Project: proj}
	seen := map[string]bool{}
	used := map[string]bool{}
	for _, extra := range roExtras {
		ro, err := resolve(extra)
		if err != nil {
			return Paths{}, fmt.Errorf("--ro: %w", err)
		}
		switch {
		case c.within(home, ro):
			return Paths{}, refuse(extra, ro, "it contains your home directory")
		case c.overlaps(ro, proj):
			return Paths{}, refuse(extra, ro, "it overlaps the project, which is already mounted at "+sandbox.Workspace)
		}
		if f, ok := c.overlapsAny(ro, forbidden); ok {
			return Paths{}, refuse(extra, ro, "it overlaps "+f+", which is never mounted")
		}
		if seen[c.key(ro)] {
			continue
		}
		seen[c.key(ro)] = true
		out.ReadOnly = append(out.ReadOnly, sandbox.Mount{Source: ro, Target: target(ro, used)})
	}
	return out, nil
}

// CheckReadOnly refuses path as a read-only mount when it contains the home
// directory or overlaps a forbidden location, as for --ro paths. It is for
// other paths a session reads, such as a linked worktree's git dir.
func (p Policy) CheckReadOnly(path string) error {
	home, err := resolve(p.Home)
	if err != nil {
		return fmt.Errorf("home directory: %w", err)
	}
	resolved, err := resolve(path)
	if err != nil {
		return err
	}
	c := checker{fold: p.CaseInsensitive}
	if c.within(home, resolved) {
		return fmt.Errorf("refusing to mount %s: it contains your home directory", path)
	}
	if f, ok := c.overlapsAny(resolved, p.forbidden()); ok {
		return fmt.Errorf("refusing to mount %s: it overlaps %s, which is never mounted", path, f)
	}
	return nil
}

// forbidden returns each forbidden path plus, where it exists, its resolved
// form: /var/run/docker.sock may be a link into ~/.rd, and /var into /private.
func (p Policy) forbidden() []string {
	var out []string
	for _, f := range p.Forbidden {
		out = append(out, filepath.Clean(f))
		if r, err := resolve(f); err == nil && r != filepath.Clean(f) {
			out = append(out, r)
		}
	}
	return out
}

// target picks /ro/<basename>, adding -2, -3, ... when the name is taken.
func target(src string, used map[string]bool) string {
	base := filepath.Base(src)
	name := base
	for i := 2; used[name]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	used[name] = true
	return sandbox.ReadOnlyRoot + "/" + name
}

func resolve(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

type checker struct{ fold bool }

func (c checker) key(p string) string {
	if c.fold {
		return strings.ToLower(p)
	}
	return p
}

func (c checker) equal(a, b string) bool { return c.key(a) == c.key(b) }

// within reports whether path is dir or inside it.
func (c checker) within(path, dir string) bool {
	path, dir = c.key(path), c.key(dir)
	if dir == "/" || path == dir {
		return true
	}
	return strings.HasPrefix(path, dir+"/")
}

func (c checker) overlaps(a, b string) bool { return c.within(a, b) || c.within(b, a) }

func (c checker) overlapsAny(path string, others []string) (string, bool) {
	for _, o := range others {
		if c.overlaps(path, o) {
			return o, true
		}
	}
	return "", false
}

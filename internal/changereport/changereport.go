// Package changereport detects changes to files that run things on the host
// (build scripts, CI workflows, editor tasks, hooks) so run can warn about
// them after a session.
package changereport

import (
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Watched lists the paths whose changes are reported, in a gitignore-like
// syntax: a trailing slash means a directory and covers everything inside
// it; a pattern containing a slash is anchored at the project root,
// otherwise it matches the name at any depth.
var Watched = []string{
	"package.json",
	"Makefile",
	"makefile",
	"GNUmakefile",
	"*.nix",
	"flake.lock",
	".envrc",
	".pre-commit-config.yaml",
	".github/workflows/",
	".vscode/",
	".devcontainer/",
	".husky/",
	".githooks/",
}

// skipDirs are not searched: dependency and build output, which is large and
// changes for reasons of its own. .git is protected by mounts instead.
var skipDirs = []string{
	".git", "node_modules", ".venv", "venv", "__pycache__", "vendor",
	"target", "dist", "build", ".next", ".nuxt", ".gradle", ".terraform",
	".direnv", ".cache",
}

// Files records the watched files of a project, keyed by slash-separated
// path relative to the project.
type Files map[string]entry

// entry identifies one file's state. Symlinks are recorded by target, not
// followed.
type entry struct {
	mode fs.FileMode
	sum  [sha256.Size]byte
	link string
}

// Kind is how a watched file changed.
type Kind string

const (
	Added    Kind = "added"
	Modified Kind = "modified"
	Deleted  Kind = "deleted"
)

// Change is one watched file that differs between two snapshots.
type Change struct {
	Path string
	Kind Kind
}

// Snapshot records the watched files in dir. Only watched files are read;
// the rest of the tree is listed but not opened.
func Snapshot(dir string) (Files, error) {
	return snapshot(dir, Watched)
}

func snapshot(dir string, watched []string) (Files, error) {
	ps := parse(watched)
	s := Files{}
	if err := s.walk(dir, "", ps, false); err != nil {
		return nil, err
	}
	return s, nil
}

// walk lists the directory rel. Inside a watched directory (all) every
// file is recorded.
func (s Files) walk(root, rel string, ps []pattern, all bool) error {
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := strings.TrimPrefix(rel+"/"+e.Name(), "/")
		isDir := e.IsDir()
		if isDir && !all && slices.Contains(skipDirs, e.Name()) {
			continue
		}
		watched := all || matches(ps, p, isDir)
		if isDir {
			if err := s.walk(root, p, ps, watched); err != nil {
				return err
			}
			continue
		}
		// A symlink matching a watched directory pattern (".vscode" made a
		// link) is recorded too: it changes what the host reads there.
		if !watched && e.Type()&fs.ModeSymlink != 0 {
			watched = matches(ps, p, true)
		}
		if watched {
			if err := s.record(root, p); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s Files) record(root, rel string) error {
	full := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(full)
	if err != nil {
		return err
	}
	e := entry{mode: info.Mode()}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		if e.link, err = os.Readlink(full); err != nil {
			return err
		}
	case info.Mode().IsRegular():
		f, err := os.Open(full)
		if err != nil {
			return err
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		copy(e.sum[:], h.Sum(nil))
	}
	s[rel] = e
	return nil
}

// Diff returns the watched files added, modified or deleted between before
// and after, sorted by path. A change of content, permissions or symlink
// target counts as modified.
func Diff(before, after Files) []Change {
	var out []Change
	for p, b := range before {
		a, ok := after[p]
		switch {
		case !ok:
			out = append(out, Change{p, Deleted})
		case a != b:
			out = append(out, Change{p, Modified})
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			out = append(out, Change{p, Added})
		}
	}
	slices.SortFunc(out, func(x, y Change) int { return strings.Compare(x.Path, y.Path) })
	return out
}

// Warnings formats changes for the user, one line each.
func Warnings(changes []Change) []string {
	out := make([]string, len(changes))
	for i, c := range changes {
		out[i] = fmt.Sprintf("%-8s  %s", c.Kind, c.Path)
	}
	return out
}

// pattern is one parsed Watched entry.
type pattern struct {
	glob     string
	dir      bool
	anchored bool
}

func parse(watched []string) []pattern {
	ps := make([]pattern, 0, len(watched))
	for _, w := range watched {
		p := pattern{dir: strings.HasSuffix(w, "/")}
		w = strings.Trim(w, "/")
		p.anchored = strings.Contains(w, "/")
		p.glob = w
		ps = append(ps, p)
	}
	return ps
}

// matches reports whether the slash-separated path rel matches a pattern.
func matches(ps []pattern, rel string, dir bool) bool {
	for _, p := range ps {
		if p.dir && !dir {
			continue
		}
		name := rel
		if !p.anchored {
			name = path.Base(rel)
		}
		if ok, _ := path.Match(p.glob, name); ok {
			return true
		}
	}
	return false
}

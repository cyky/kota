// Package changereport detects changes to files that run things on the host
// (build scripts, CI workflows, editor tasks, hooks) so run can warn about
// them after a session.
package changereport

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"kota/internal/deps"
)

// Watched lists the paths whose changes are reported, in a gitignore-like
// syntax: a trailing slash means a directory and covers everything inside
// it; a pattern containing a slash is anchored at the project root,
// otherwise it matches the name at any depth.
var Watched = []string{
	"package.json",
	// The go and toolchain lines pick a toolchain go downloads and runs,
	// and tool lines name commands go tool runs.
	"go.mod",
	"Makefile",
	"makefile",
	"GNUmakefile",
	"*.nix",
	"flake.lock",
	".envrc",
	// mise runs hooks and sets the environment on cd, like direnv.
	"mise.toml",
	".mise.toml",
	"mise.local.toml",
	".mise.local.toml",
	".tool-versions",
	// Select filter and diff drivers, and submodule URLs.
	".gitattributes",
	".gitmodules",
	// cargo reads .cargo/config.toml in every directory up from where it
	// runs, and runs build.rs.
	".cargo/",
	"build.rs",
	"justfile",
	"Justfile",
	".justfile",
	"Taskfile.yml",
	"Taskfile.yaml",
	".pre-commit-config.yaml",
	".github/workflows/",
	".vscode/",
	".idea/",
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
	// content is kept for a manifest deps can compare, up to maxContent
	// bytes; it is nil for a larger one.
	content []byte
}

// maxContent is the largest manifest whose content a snapshot keeps.
const maxContent = 1 << 20

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

// SnapshotWith is Snapshot plus the files extra, relative to dir with
// forward slashes, wherever they are. A missing one is left out, so
// creating it counts as added.
func SnapshotWith(dir string, extra []string) (Files, error) {
	s, err := Snapshot(dir)
	if err != nil {
		return nil, err
	}
	for _, rel := range extra {
		info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil || info.IsDir() {
			continue
		}
		if err := s.record(dir, rel); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// SnapshotMatching records the files in dir that match patterns, in the
// syntax of Watched.
func SnapshotMatching(dir string, patterns []string) (Files, error) {
	return snapshot(dir, patterns)
}

// SnapshotAll records every file in dir outside the skipped directories,
// which are skipped at any depth.
func SnapshotAll(dir string) (Files, error) {
	s := Files{}
	var walk func(rel string) error
	walk = func(rel string) error {
		entries, err := os.ReadDir(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		for _, e := range entries {
			p := strings.TrimPrefix(rel+"/"+e.Name(), "/")
			if !e.IsDir() {
				if err := s.record(dir, p); err != nil {
					return err
				}
			} else if !slices.Contains(skipDirs, e.Name()) {
				if err := walk(p); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(""); err != nil {
		return nil, err
	}
	return s, nil
}

// Digests returns each file's state as a string, for storing a snapshot.
func (s Files) Digests() map[string]string {
	out := make(map[string]string, len(s))
	for p, e := range s {
		out[p] = fmt.Sprintf("%o:%x:%s", uint32(e.mode), e.sum, e.link)
	}
	return out
}

// DiffDigests is Diff for snapshots stored with Digests.
func DiffDigests(before, after map[string]string) []Change {
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
		var w io.Writer = h
		var kept capped
		if deps.IsManifest(rel) {
			w = io.MultiWriter(h, &kept)
		}
		if _, err := io.Copy(w, f); err != nil {
			return err
		}
		copy(e.sum[:], h.Sum(nil))
		if deps.IsManifest(rel) && !kept.over {
			// Not nil, even for an empty file.
			e.content = append([]byte{}, kept.buf.Bytes()...)
		}
	}
	s[rel] = e
	return nil
}

// Diff returns the watched files added, modified or deleted between before
// and after, sorted by path. A change of content, permissions or symlink
// target counts as modified.
func Diff(before, after Files) []Change {
	return DiffDigests(before.Digests(), after.Digests())
}

// capped is a writer that keeps up to maxContent bytes and notes whether
// more were written.
type capped struct {
	buf  bytes.Buffer
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	if !c.over && c.buf.Len()+len(p) <= maxContent {
		c.buf.Write(p)
	} else {
		c.over = true
		c.buf.Reset()
	}
	return len(p), nil
}

// Warnings formats changes for the user, one line each.
func Warnings(changes []Change) []string {
	out := make([]string, len(changes))
	for i, c := range changes {
		out[i] = fmt.Sprintf("%-8s  %s", c.Kind, printable(c.Path))
	}
	return out
}

// detailIndent indents Report's lines about a change under its line.
const detailIndent = "    "

// Report is Warnings with each added or modified file followed by indented
// lines that say what changed or where to look: a modified manifest's
// dependency and script changes, else the git command that shows the
// change. before and after are the snapshots changes came from; project is
// the host directory the commands name.
func Report(project string, before, after Files, changes []Change) []string {
	var out []string
	for i, line := range Warnings(changes) {
		out = append(out, line)
		for _, d := range details(project, before, after, changes[i]) {
			out = append(out, detailIndent+d)
		}
	}
	return out
}

func details(project string, before, after Files, c Change) []string {
	switch c.Kind {
	case Deleted:
		return nil
	case Added:
		return []string{gitCommand(project, "status", c.Path)}
	}
	if !deps.IsManifest(c.Path) {
		return []string{gitCommand(project, "diff HEAD", c.Path)}
	}
	b, a := before[c.Path].content, after[c.Path].content
	if b == nil || a == nil {
		return []string{"could not compare the dependencies: not a regular file, or too large", gitCommand(project, "diff HEAD", c.Path)}
	}
	lines, err := deps.ManifestChanges(c.Path, b, a)
	if err != nil {
		// The parse error may quote the file, so it isn't shown.
		return []string{"could not compare the dependencies: the file can't be parsed before or after the session", gitCommand(project, "diff HEAD", c.Path)}
	}
	if len(lines) == 0 {
		return []string{"no dependency or script changes", gitCommand(project, "diff HEAD", c.Path)}
	}
	return lines
}

// gitCommand is the line telling the user to run git sub on rel in
// project, with both paths quoted for the shell.
func gitCommand(project, sub, rel string) string {
	if printable(project) != project || printable(rel) != rel {
		return "see: git status (the path has control characters)"
	}
	return fmt.Sprintf("see: git -C %s %s -- %s", shellQuote(project), sub, shellQuote(rel))
}

// printable returns s as it is when every character is printable, else
// quoted with the rest escaped, so a path can't write control sequences to
// the terminal.
func printable(s string) string {
	if strings.ContainsFunc(s, func(c rune) bool { return !unicode.IsPrint(c) }) {
		return strconv.Quote(s)
	}
	return s
}

// shellQuote quotes s for sh when it holds anything but plain path
// characters.
func shellQuote(s string) string {
	plain := s != "" && !strings.ContainsFunc(s, func(c rune) bool {
		return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("/._-+@", c))
	})
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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

package mask

import (
	"strings"
)

// Excludes returns a gitignore file, for core.excludesFile inside the
// sandbox, that lists every placeholder and mask outside .git, anchored at
// the project root. git add -A then skips the empty files mounted there.
// A tracked masked file isn't affected: excludes only apply to untracked
// paths, so only the commit check catches it recorded emptied.
func (l Layout) Excludes() string {
	var b strings.Builder
	for _, m := range l.Mounts {
		if !m.Placeholder && !m.Mask || InGitDir(m.Path) || strings.ContainsAny(m.Path, "\n\r") {
			continue
		}
		b.WriteString("/" + escapeGlob(m.Path))
		if m.Dir {
			b.WriteString("/")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// InGitDir reports whether the slash-separated relative path is .git or
// inside it, where nothing is ever committed.
func InGitDir(rel string) bool {
	return rel == ".git" || strings.HasPrefix(rel, ".git/")
}

// escapeGlob makes rel match only itself as a gitignore pattern.
func escapeGlob(rel string) string {
	var b strings.Builder
	for i, c := range rel {
		switch {
		case strings.ContainsRune(`\*?[`, c),
			i == 0 && (c == '!' || c == '#'),
			c == ' ' && strings.TrimRight(rel[i:], " ") == "":
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// Ignore is a parsed gitignore file, such as the host's global excludes.
type Ignore struct{ rs rules }

// ParseIgnore parses data in gitignore syntax. source names it in errors.
func ParseIgnore(data []byte, source string) (Ignore, error) {
	rs, err := parseRules(strings.Split(string(data), "\n"), source)
	return Ignore{rs}, err
}

// Ignored reports whether git would ignore the file at the slash-separated
// relative path: the file or a directory on the way to it is matched.
func (ig Ignore) Ignored(rel string) bool {
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		if ig.rs.excluded(strings.Join(parts[:i], "/"), true) {
			return true
		}
	}
	return ig.rs.excluded(rel, false)
}

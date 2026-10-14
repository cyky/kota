package mask

import (
	"fmt"
	"regexp"
	"strings"
)

// rule is one gitignore-style pattern.
type rule struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
	source  string
}

// rules is an ordered pattern list; the last matching rule decides.
type rules []rule

// parseRules parses gitignore-syntax lines. source names where they came
// from, for error messages.
func parseRules(lines []string, source string) (rules, error) {
	var out rules
	for i, line := range lines {
		r, ok, err := parseRule(line)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", source, i+1, err)
		}
		if ok {
			r.source = source
			out = append(out, r)
		}
	}
	return out, nil
}

func parseRule(line string) (rule, bool, error) {
	line = strings.TrimSuffix(line, "\r")
	line = trimTrailingSpaces(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return rule{}, false, nil
	}
	var r rule
	if strings.HasPrefix(line, "!") {
		r.negate = true
		line = line[1:]
	} else if strings.HasPrefix(line, `\!`) || strings.HasPrefix(line, `\#`) {
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") && !strings.HasSuffix(line, `\/`) {
		r.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	if line == "" {
		return rule{}, false, nil
	}
	// A slash at the start or in the middle anchors the pattern to the
	// project root; otherwise it matches at any depth.
	anchored := strings.Contains(line, "/")
	line = strings.TrimPrefix(line, "/")

	body, err := globRegexp(line)
	if err != nil {
		return rule{}, false, err
	}
	prefix := `^(?:.*/)?`
	if anchored {
		prefix = `^`
	}
	r.re, err = regexp.Compile(prefix + body + `$`)
	if err != nil {
		return rule{}, false, err
	}
	return r, true, nil
}

// trimTrailingSpaces drops trailing spaces unless escaped with a backslash.
func trimTrailingSpaces(s string) string {
	for strings.HasSuffix(s, " ") && !strings.HasSuffix(s, `\ `) {
		s = s[:len(s)-1]
	}
	return s
}

// globRegexp translates a gitignore glob to a regexp body.
func globRegexp(glob string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case c == '*' && strings.HasPrefix(glob[i:], "**"):
			atStart := i == 0 || glob[i-1] == '/'
			rest := glob[i+2:]
			switch {
			case atStart && strings.HasPrefix(rest, "/"):
				b.WriteString(`(?:.*/)?`) // "**/": any leading directories
				i += 2
			case atStart && rest == "":
				b.WriteString(`.*`) // trailing "/**": everything inside
				i++
			default:
				b.WriteString(`[^/]*`) // other "**" is a plain "*"
				i++
			}
		case c == '*':
			b.WriteString(`[^/]*`)
		case c == '?':
			b.WriteString(`[^/]`)
		case c == '[':
			end := strings.IndexByte(glob[i+1:], ']')
			if end < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := glob[i+1 : i+1+end]
			if end == 0 { // "[]...]": the first ] is literal
				next := strings.IndexByte(glob[i+2:], ']')
				if next < 0 {
					b.WriteString(`\[`)
					continue
				}
				class = glob[i+1 : i+2+next]
				end = next + 1
			}
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += end + 1
		case c == '\\' && i+1 < len(glob):
			i++
			b.WriteString(regexp.QuoteMeta(string(glob[i])))
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String(), nil
}

// excluded reports whether the slash-separated relative path is matched,
// after negations. The caller doesn't descend into excluded directories,
// which gives gitignore's rule that nothing inside one can be re-included.
func (rs rules) excluded(path string, dir bool) bool {
	match := false
	for _, r := range rs {
		if r.dirOnly && !dir {
			continue
		}
		// A directory also matches as "dir/", as in git, so "vault/**"
		// covers the vault directory itself.
		if r.re.MatchString(path) || dir && r.re.MatchString(path+"/") {
			match = !r.negate
		}
	}
	return match
}

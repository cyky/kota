package deps

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/mod/modfile"
)

// IsManifest reports whether ManifestChanges can compare the file, by its
// slash-separated path.
func IsManifest(file string) bool {
	switch path.Base(file) {
	case "package.json", "go.mod":
		return true
	}
	return false
}

// ManifestChanges lists what changed between two versions of a manifest
// that IsManifest accepts, one line each: package.json dependencies and
// scripts, with the install's lifecycle scripts marked, and go.mod's go,
// toolchain, require, replace and tool lines. It returns an error when
// either version can't be parsed. Names and values are quoted, so control
// characters in the files are escaped.
func ManifestChanges(file string, before, after []byte) ([]string, error) {
	if path.Base(file) == "go.mod" {
		return goModChanges(file, before, after)
	}
	return packageChanges(before, after)
}

// packageManifest is the part of package.json ManifestChanges compares.
type packageManifest struct {
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
	Scripts              map[string]string `json:"scripts"`
}

func packageChanges(before, after []byte) ([]string, error) {
	var b, a packageManifest
	if err := json.Unmarshal(before, &b); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(after, &a); err != nil {
		return nil, err
	}
	var out []string
	out = append(out, mapChanges("dependencies", b.Dependencies, a.Dependencies, nil)...)
	out = append(out, mapChanges("devDependencies", b.DevDependencies, a.DevDependencies, nil)...)
	out = append(out, mapChanges("optionalDependencies", b.OptionalDependencies, a.OptionalDependencies, nil)...)
	out = append(out, mapChanges("peerDependencies", b.PeerDependencies, a.PeerDependencies, nil)...)
	hook := func(name string) string {
		if slices.Contains(lifecycleScripts, name) {
			return " (runs on install)"
		}
		return ""
	}
	out = append(out, mapChanges("scripts", b.Scripts, a.Scripts, hook)...)
	return out, nil
}

func goModChanges(file string, before, after []byte) ([]string, error) {
	b, err := modfile.Parse(file, before, nil)
	if err != nil {
		return nil, err
	}
	a, err := modfile.Parse(file, after, nil)
	if err != nil {
		return nil, err
	}
	var out []string
	// single compares a directive that appears at most once; "" is absent.
	single := func(name string, bv, av string, note string) {
		entry := func(v string) map[string]string {
			if v == "" {
				return nil
			}
			return map[string]string{name: v}
		}
		out = append(out, mapChanges("", entry(bv), entry(av), func(string) string { return note })...)
	}
	goVersion := func(f *modfile.File) string {
		if f.Go == nil {
			return ""
		}
		return f.Go.Version
	}
	toolchain := func(f *modfile.File) string {
		if f.Toolchain == nil {
			return ""
		}
		return f.Toolchain.Name
	}
	const download = " (go may download and run this toolchain)"
	single("go", goVersion(b), goVersion(a), download)
	single("toolchain", toolchain(b), toolchain(a), download)
	out = append(out, mapChanges("require", requires(b), requires(a), nil)...)
	out = append(out, mapChanges("replace", replaces(b), replaces(a), nil)...)
	out = append(out, mapChanges("tool", tools(b), tools(a), func(string) string { return " (go tool runs it)" })...)
	return out, nil
}

func requires(f *modfile.File) map[string]string {
	m := map[string]string{}
	for _, r := range f.Require {
		m[r.Mod.Path] = r.Mod.Version
	}
	return m
}

func replaces(f *modfile.File) map[string]string {
	m := map[string]string{}
	for _, r := range f.Replace {
		m[modString(r.Old.Path, r.Old.Version)] = modString(r.New.Path, r.New.Version)
	}
	return m
}

func tools(f *modfile.File) map[string]string {
	m := map[string]string{}
	for _, t := range f.Tool {
		m[t.Path] = ""
	}
	return m
}

func modString(p, version string) string {
	if version == "" {
		return p
	}
	return p + " " + version
}

// mapChanges lists the entries added, removed or changed between before
// and after, sorted by name, under the heading field ("" for none). note
// adds a remark after an entry's name.
func mapChanges(field string, before, after map[string]string, note func(string) string) []string {
	names := make([]string, 0, len(before)+len(after))
	for n := range before {
		names = append(names, n)
	}
	for n := range after {
		if _, ok := before[n]; !ok {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	prefix := ""
	if field != "" {
		prefix = field + ": "
	}
	var out []string
	for _, n := range names {
		bv, inBefore := before[n]
		av, inAfter := after[n]
		label := quote(n)
		if note != nil {
			label += note(n)
		}
		switch {
		case !inBefore:
			out = append(out, fmt.Sprintf("%sadded %s%s", prefix, label, value(av)))
		case !inAfter:
			out = append(out, fmt.Sprintf("%sremoved %s%s", prefix, label, value(bv)))
		case bv != av:
			out = append(out, fmt.Sprintf("%schanged %s: %s -> %s", prefix, label, quote(bv), quote(av)))
		}
	}
	return out
}

func value(v string) string {
	if v == "" {
		return ""
	}
	return " " + quote(v)
}

// maxValue is how many characters of a name or value are shown.
const maxValue = 120

// quote returns s for the terminal: as it is when it's short and plainly
// printable, else quoted with control characters escaped and cut to
// maxValue characters.
func quote(s string) string {
	r := []rune(s)
	cut := len(r) > maxValue
	if cut {
		r = r[:maxValue]
	}
	plain := !cut && s != "" && !strings.ContainsFunc(s, func(c rune) bool {
		return !unicode.IsPrint(c) || unicode.IsSpace(c) || c == '"' || c == '\\'
	})
	if plain {
		return s
	}
	q := strconv.Quote(string(r))
	if cut {
		q += "..."
	}
	return q
}

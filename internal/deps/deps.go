// Package deps works out how prep installs a project's dependencies: the
// install commands, and the dependency directories that live in Docker
// volumes instead of the project directory.
//
// Detection covers npm, pnpm and yarn (run through corepack) and Go modules.
// A project's ConfigFile can replace the detected commands and add
// dependency directories.
package deps

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ConfigFile is the per-project config at the project root. Its prep
// command runs with network access, so it is read-only inside the sandbox.
const ConfigFile = "localllm.yaml"

// Step is one install command, run by sh in the project root.
type Step struct {
	// Name says what the step installs, e.g. "pnpm" or ConfigFile.
	Name   string
	Script string
}

// Dir is a dependency directory, relative to the project with forward
// slashes, kept in a per-project volume mounted over it.
type Dir struct {
	Path string
	// Manifest is the file whose dependencies end up in Path, for the hint
	// shown when Path is empty.
	Manifest string
}

// Plan is how prep installs a project's dependencies.
type Plan struct {
	Steps []Step
	Dirs  []Dir
}

// Options are the settings Detect needs from the sandbox.
type Options struct {
	// PnpmStore is the in-sandbox path of the shared pnpm store.
	PnpmStore string
}

// config is the parsed ConfigFile.
type config struct {
	Prep struct {
		// Command replaces the detected install steps.
		Command string `yaml:"command"`
		// Dirs are extra dependency directories.
		Dirs []string `yaml:"dirs"`
	} `yaml:"prep"`
}

// skipDirs are not searched for manifests: dependency and build output,
// test data, and anything a mask hides.
var skipDirs = []string{
	".git", "node_modules", ".venv", "venv", "__pycache__", "vendor",
	"target", "dist", "build", ".next", ".nuxt", ".gradle", ".terraform",
	".direnv", ".cache", "testdata",
}

// Detect inspects project and returns its install plan. A project with
// nothing to install gets an empty plan.
func Detect(project string, opts Options) (Plan, error) {
	cfg, err := readConfig(project)
	if err != nil {
		return Plan{}, err
	}
	var plan Plan
	if err := plan.node(project, opts); err != nil {
		return Plan{}, err
	}
	if err := plan.golang(project); err != nil {
		return Plan{}, err
	}
	if cfg.Prep.Command != "" {
		plan.Steps = []Step{{Name: ConfigFile, Script: cfg.Prep.Command}}
	}
	for _, d := range cfg.Prep.Dirs {
		clean, err := cleanDir(d)
		if err != nil {
			return Plan{}, fmt.Errorf("%s: prep.dirs: %w", ConfigFile, err)
		}
		if !slices.ContainsFunc(plan.Dirs, func(o Dir) bool { return o.Path == clean }) {
			plan.Dirs = append(plan.Dirs, Dir{Path: clean, Manifest: ConfigFile})
		}
	}
	for _, d := range plan.Dirs {
		if err := noSymlinks(project, d.Path); err != nil {
			return Plan{}, err
		}
	}
	return plan, nil
}

func readConfig(project string) (config, error) {
	var cfg config
	data, err := os.ReadFile(filepath.Join(project, ConfigFile))
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	} else if err != nil {
		return cfg, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return cfg, fmt.Errorf("parse %s: %w", ConfigFile, err)
	}
	cfg.Prep.Command = strings.TrimSpace(cfg.Prep.Command)
	return cfg, nil
}

// cleanDir validates a dependency directory from ConfigFile. It must stay
// inside the project and out of .git, whose protections a volume would
// hide.
func cleanDir(d string) (string, error) {
	clean := path.Clean(strings.TrimSuffix(d, "/"))
	switch {
	case d == "" || path.IsAbs(d) || strings.Contains(d, `\`):
		return "", fmt.Errorf("%q: must be a relative path", d)
	case clean == "." || clean == ".." || strings.HasPrefix(clean, "../"):
		return "", fmt.Errorf("%q: must be inside the project", d)
	case clean == ".git" || strings.HasPrefix(clean, ".git/"):
		return "", fmt.Errorf("%q: must not be in .git", d)
	}
	return clean, nil
}

// Conflict returns an error when a volume over a dependency directory would
// hide or replace a protected path: when the directory is, contains or lies
// inside one of protected, or is or contains one of pinned. Paths are
// project-relative and slash-separated.
func (p Plan) Conflict(protected, pinned []string) error {
	for _, d := range p.Dirs {
		for _, o := range protected {
			if d.Path == o || strings.HasPrefix(o, d.Path+"/") || strings.HasPrefix(d.Path, o+"/") {
				return fmt.Errorf("dependency directory %s overlaps the protected or masked path %s", d.Path, o)
			}
		}
		for _, o := range pinned {
			if d.Path == o || strings.HasPrefix(o, d.Path+"/") {
				return fmt.Errorf("dependency directory %s overlaps the protected path %s", d.Path, o)
			}
		}
	}
	return nil
}

// noSymlinks refuses a dependency directory with a symlink on the way to
// it: Docker would follow it when creating the mount point.
func noSymlinks(project, rel string) error {
	p := project
	for _, part := range strings.Split(rel, "/") {
		p = filepath.Join(p, part)
		info, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("refusing dependency directory %s: %s is a symlink", rel, filepath.ToSlash(strings.TrimPrefix(p, project+"/")))
		}
		if !info.IsDir() {
			return fmt.Errorf("refusing dependency directory %s: %s is not a directory", rel, filepath.ToSlash(strings.TrimPrefix(p, project+"/")))
		}
	}
	return nil
}

// packageJSON is the part of package.json detection reads.
type packageJSON struct {
	PackageManager string          `json:"packageManager"`
	Workspaces     json.RawMessage `json:"workspaces"`
}

// node adds the install step for a package.json at the project root, and a
// node_modules directory for the root and each workspace package.
func (p *Plan) node(project string, opts Options) error {
	data, err := os.ReadFile(filepath.Join(project, "package.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return fmt.Errorf("parse package.json: %w", err)
	}
	manager, version := managerFromField(pkg.PackageManager)
	if manager == "" {
		manager = managerFromLockfile(project)
	}
	switch manager {
	case "pnpm":
		script := "pnpm install --store-dir " + opts.PnpmStore
		if exists(project, "pnpm-lock.yaml") {
			script += " --frozen-lockfile"
		}
		p.Steps = append(p.Steps, Step{Name: "pnpm", Script: script})
	case "yarn":
		berry := version != "" && !strings.HasPrefix(version, "1.") || exists(project, ".yarnrc.yml")
		script := "yarn install"
		switch {
		case !exists(project, "yarn.lock"):
		case berry:
			script += " --immutable"
		default:
			script += " --frozen-lockfile"
		}
		p.Steps = append(p.Steps, Step{Name: "yarn", Script: script})
	case "npm":
		script := "npm install"
		if exists(project, "package-lock.json") || exists(project, "npm-shrinkwrap.json") {
			script = "npm ci"
		}
		p.Steps = append(p.Steps, Step{Name: "npm", Script: script})
	default:
		return fmt.Errorf("package.json: unsupported packageManager %q; set prep.command in %s", pkg.PackageManager, ConfigFile)
	}

	p.Dirs = append(p.Dirs, Dir{Path: "node_modules", Manifest: "package.json"})
	patterns, err := workspacePatterns(project, manager, pkg.Workspaces)
	if err != nil {
		return err
	}
	if len(patterns) == 0 {
		return nil
	}
	return walk(project, func(rel string, names []string) {
		if rel != "" && slices.Contains(names, "package.json") && matchWorkspace(patterns, rel) {
			p.Dirs = append(p.Dirs, Dir{Path: rel + "/node_modules", Manifest: rel + "/package.json"})
		}
	})
}

// managerFromField reads the packageManager field, e.g. "pnpm@9.1.0+sha...".
func managerFromField(field string) (name, version string) {
	if field == "" {
		return "", ""
	}
	name, version, _ = strings.Cut(field, "@")
	if name != "npm" && name != "pnpm" && name != "yarn" {
		return field, ""
	}
	return name, version
}

func managerFromLockfile(project string) string {
	switch {
	case exists(project, "pnpm-lock.yaml"):
		return "pnpm"
	case exists(project, "yarn.lock"):
		return "yarn"
	}
	return "npm"
}

// workspacePatterns returns the workspace package globs: pnpm reads them
// from pnpm-workspace.yaml, npm and yarn from package.json.
func workspacePatterns(project, manager string, field json.RawMessage) ([]string, error) {
	if manager == "pnpm" {
		data, err := os.ReadFile(filepath.Join(project, "pnpm-workspace.yaml"))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		var ws struct {
			Packages []string `yaml:"packages"`
		}
		if err := yaml.Unmarshal(data, &ws); err != nil {
			return nil, fmt.Errorf("parse pnpm-workspace.yaml: %w", err)
		}
		return ws.Packages, nil
	}
	if len(field) == 0 {
		return nil, nil
	}
	// Either a list, or yarn's {"packages": [...]}.
	var list []string
	if err := json.Unmarshal(field, &list); err == nil {
		return list, nil
	}
	var obj struct {
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(field, &obj); err != nil {
		return nil, fmt.Errorf("parse package.json workspaces: %w", err)
	}
	return obj.Packages, nil
}

// matchWorkspace reports whether the directory rel matches the workspace
// globs. A "!" pattern excludes; the last matching pattern wins.
func matchWorkspace(patterns []string, rel string) bool {
	matched := false
	for _, pat := range patterns {
		neg := strings.HasPrefix(pat, "!")
		pat = path.Clean(strings.TrimPrefix(strings.TrimPrefix(pat, "!"), "./"))
		if globMatch(strings.Split(pat, "/"), strings.Split(rel, "/")) {
			matched = !neg
		}
	}
	return matched
}

// globMatch matches path segments, where a "**" segment matches any number
// of segments.
func globMatch(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if globMatch(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	ok, err := path.Match(pat[0], segs[0])
	return err == nil && ok && globMatch(pat[1:], segs[1:])
}

// golang adds a module download for each Go module. With a go.work at the
// root, one download in the root covers every module of the workspace.
func (p *Plan) golang(project string) error {
	if exists(project, "go.work") {
		p.Steps = append(p.Steps, Step{Name: "go", Script: "go mod download"})
		return nil
	}
	var dirs []string
	err := walk(project, func(rel string, names []string) {
		if slices.Contains(names, "go.mod") {
			dirs = append(dirs, rel)
		}
	})
	for _, d := range dirs {
		script := "go mod download"
		if d != "" {
			script = "(cd " + shellQuote(d) + " && go mod download)"
		}
		p.Steps = append(p.Steps, Step{Name: "go", Script: script})
	}
	return err
}

// walk calls fn for each directory of the project (rel is "" for the root)
// with the names of its entries, skipping skipDirs and symlinks.
func walk(project string, fn func(rel string, names []string)) error {
	var visit func(rel string) error
	visit = func(rel string) error {
		entries, err := os.ReadDir(filepath.Join(project, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		fn(rel, names)
		for _, e := range entries {
			if !e.IsDir() || slices.Contains(skipDirs, e.Name()) {
				continue
			}
			if err := visit(strings.TrimPrefix(rel+"/"+e.Name(), "/")); err != nil {
				return err
			}
		}
		return nil
	}
	return visit("")
}

// Script returns the shell script that runs every step in order and stops
// at the first failure.
func (p Plan) Script() string {
	var b strings.Builder
	b.WriteString("set -e\n")
	for _, s := range p.Steps {
		fmt.Fprintf(&b, "echo %s\n%s\n", shellQuote("==> "+s.Name+": "+s.Script), s.Script)
	}
	return b.String()
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func exists(project, name string) bool {
	_, err := os.Lstat(filepath.Join(project, name))
	return err == nil
}

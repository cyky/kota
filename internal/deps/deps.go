// Package deps works out how prep installs a project's dependencies: the
// fetch and install commands, and the dependency directories that live in
// Docker volumes instead of the project directory.
//
// Prep runs in two phases. Fetch commands run only package manager code,
// which checks what it downloads against the project's lockfiles, and fill
// the caches shared by all projects. Install commands may run project code
// (lifecycle scripts, ConfigFile's command) and see those caches read-only.
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
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ConfigFile is the per-project config at the project root. Its prep
// command runs with network access, so it is read-only inside the sandbox.
const ConfigFile = "kota.yaml"

// Step is one package manager's commands, each run by sh in the project
// root. Either may be empty.
type Step struct {
	// Name says what the step installs, e.g. "pnpm" or ConfigFile.
	Name string
	// Fetch fills the shared caches. It must not run project code.
	Fetch string
	// Script installs into the project's dependency directories.
	Script string
}

// Dir is a dependency directory, relative to the project with forward
// slashes, kept in a per-project volume mounted over it.
type Dir struct {
	Path string `json:"path"`
	// Manifest is the file whose dependencies end up in Path, for the hint
	// shown when Path is empty.
	Manifest string `json:"manifest"`
}

// Plan is how prep installs a project's dependencies.
type Plan struct {
	Steps []Step
	Dirs  []Dir
	// Hooks describe the project code the install runs: lifecycle scripts,
	// pnpmfiles, a yarnPath, ConfigFile's command. One line each.
	Hooks []string
	// Fallback is set on a plan from Fallback, and says why and what it
	// mounts.
	Fallback string
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
	// Engine is parsed by package engine.
	Engine yaml.Node `yaml:"engine"`
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
		// The detected fetches still fill the caches the command can use.
		for i := range plan.Steps {
			plan.Steps[i].Script = ""
		}
		plan.Steps = append(plan.Steps, Step{Name: ConfigFile, Script: cfg.Prep.Command})
		plan.Hooks = append(plan.Hooks, ConfigFile+": prep.command: "+cfg.Prep.Command)
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

// Fallback is the plan for a session when Detect failed with cause, e.g.
// on a manifest left broken: it has no install steps, and volumes over last,
// the directories of the last successful prep, or the root node_modules if
// there is none. Every other node_modules found on the host gets a volume
// too, so none of them is visible or writable in the sandbox.
func Fallback(project string, last []Dir, cause error) (Plan, error) {
	plan := Plan{Fallback: fmt.Sprintf("can't work out the dependencies (%v); using the dependency directories of the last prep", cause)}
	if len(last) == 0 {
		last = []Dir{{Path: "node_modules", Manifest: "package.json"}}
		plan.Fallback = fmt.Sprintf("can't work out the dependencies (%v); the project has no prep record, so only node_modules directories get volumes", cause)
	}
	plan.Fallback += ". Fix the file on the host or in the session, then run 'kota prep'"
	add := func(d Dir) {
		if !slices.ContainsFunc(plan.Dirs, func(o Dir) bool { return o.Path == d.Path }) {
			plan.Dirs = append(plan.Dirs, d)
		}
	}
	for _, d := range last {
		clean, err := cleanDir(d.Path)
		if err != nil {
			return Plan{}, fmt.Errorf("prep record: %w", err)
		}
		add(Dir{Path: clean, Manifest: d.Manifest})
	}
	// Only .git and node_modules are skipped: a node_modules missed here
	// would be the host's, in the sandbox.
	err := walkSkipping(project, []string{".git", "node_modules"}, func(rel string, names []string) {
		if slices.Contains(names, "node_modules") {
			add(Dir{Path: path.Join(rel, "node_modules"), Manifest: path.Join(rel, "package.json")})
		}
	})
	if err != nil {
		return Plan{}, err
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
	PackageManager       string            `json:"packageManager"`
	Workspaces           json.RawMessage   `json:"workspaces"`
	Scripts              map[string]string `json:"scripts"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
}

// lifecycleScripts are the package.json scripts a package manager runs for
// the project itself during install.
var lifecycleScripts = []string{
	"preinstall", "install", "postinstall", "prepublish",
	"preprepare", "prepare", "postprepare", "pnpm:devPreinstall",
}

// node adds the fetch and install commands for a package.json at the
// project root, and a node_modules directory for the root and each
// workspace package.
func (p *Plan) node(project string, opts Options) error {
	pkg, ok, err := readPackage(project, "")
	if err != nil || !ok {
		return err
	}
	manager, version, err := managerFromField(pkg.PackageManager)
	if err != nil {
		return err
	}
	if manager == "" {
		manager = managerFromLockfile(project)
	}
	// corepack downloads the package manager in the fetch phase. A
	// release named in the packageManager field is checked against its
	// hash, if the field has one.
	corepack := "corepack install"
	if pkg.PackageManager == "" {
		corepack = "corepack install -g " + map[string]string{"pnpm": "pnpm", "yarn": "yarn@1"}[manager]
	}
	switch manager {
	case "pnpm":
		step := Step{Name: "pnpm"}
		if exists(project, "pnpm-lock.yaml") {
			// pnpm fetch fills the shared store from the lockfile without
			// running any scripts; the pnpmfile is project code.
			step.Fetch = corepack + " && pnpm fetch --ignore-pnpmfile --store-dir " + opts.PnpmStore
			step.Script = "pnpm install --offline --frozen-lockfile --store-dir " + opts.PnpmStore
		} else {
			// Without a lockfile there is nothing to fetch ahead, so this
			// install uses a store of its own, discarded afterwards.
			step.Fetch = corepack
			step.Script = `pnpm install --store-dir "$HOME/.pnpm-store"`
		}
		p.Steps = append(p.Steps, step)
		for _, f := range []string{".pnpmfile.cjs", ".pnpmfile.mjs"} {
			if exists(project, f) {
				p.Hooks = append(p.Hooks, f+": pnpm hooks")
			}
		}
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
		p.Steps = append(p.Steps, Step{Name: "yarn", Fetch: corepack, Script: script})
		if yarnPath(project) != "" {
			p.Hooks = append(p.Hooks, ".yarnrc.yml: yarnPath: "+yarnPath(project))
		}
	case "npm":
		script := "npm install"
		if exists(project, "package-lock.json") || exists(project, "npm-shrinkwrap.json") {
			script = "npm ci"
		}
		p.Steps = append(p.Steps, Step{Name: "npm", Script: script})
	}

	p.Dirs = append(p.Dirs, Dir{Path: "node_modules", Manifest: "package.json"})
	p.scriptHooks("", pkg)
	// The packages whose scripts are already listed, by directory.
	seen := map[string]packageJSON{"": pkg}
	patterns, err := workspacePatterns(project, manager, pkg.Workspaces)
	if err != nil {
		return err
	}
	var dirs []string
	if len(patterns) > 0 {
		err = walk(project, func(rel string, names []string) {
			if rel != "" && slices.Contains(names, "package.json") && matchWorkspace(patterns, rel) {
				dirs = append(dirs, rel)
			}
		})
		if err != nil {
			return err
		}
	}
	for _, rel := range dirs {
		p.Dirs = append(p.Dirs, Dir{Path: rel + "/node_modules", Manifest: rel + "/package.json"})
		ws, _, err := readPackage(project, rel)
		if err != nil {
			return err
		}
		p.scriptHooks(rel, ws)
		seen[rel] = ws
	}
	queue := append([]string{""}, dirs...)
	for len(queue) > 0 {
		rel := queue[0]
		queue = queue[1:]
		more, err := p.dependencyHooks(project, rel, seen[rel], seen)
		if err != nil {
			return err
		}
		queue = append(queue, more...)
	}
	return nil
}

// localPrefixes mark dependencies installed from a directory or tarball in
// the project, whose lifecycle scripts the install runs.
var localPrefixes = []string{"file:", "link:", "portal:", "./", "../"}

// gitPrefixes mark dependencies built from a git repository, whose prepare
// script the install runs.
var gitPrefixes = []string{"git+", "git:", "github:", "gitlab:", "bitbucket:", "gist:"}

// githubShorthand is npm's "user/repo" form of a GitHub dependency.
var githubShorthand = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(#.*)?$`)

// dependencyHooks adds the project code that the dependencies of the
// package in rel run during install: each git dependency, and the lifecycle
// scripts of each local one. It returns the local package directories not
// in seen, whose own dependencies are installed too.
func (p *Plan) dependencyHooks(project, rel string, pkg packageJSON, seen map[string]packageJSON) ([]string, error) {
	manifest := path.Join(rel, "package.json")
	var more []string
	for _, field := range []map[string]string{pkg.Dependencies, pkg.DevDependencies, pkg.OptionalDependencies} {
		names := make([]string, 0, len(field))
		for name := range field {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			spec := field[name]
			if isGitSpec(spec) {
				p.Hooks = append(p.Hooks, fmt.Sprintf("%s: git dependency %s: %s (its prepare script runs)", manifest, name, spec))
				continue
			}
			i := slices.IndexFunc(localPrefixes, func(pre string) bool { return strings.HasPrefix(spec, pre) })
			if i < 0 {
				continue
			}
			local := spec
			if strings.HasSuffix(localPrefixes[i], ":") {
				local = strings.TrimPrefix(spec, localPrefixes[i])
			}
			target := path.Join(rel, local)
			if path.IsAbs(local) || target == ".." || strings.HasPrefix(target, "../") {
				// Outside the project, so not in the sandbox.
				continue
			}
			if target == "." {
				target = ""
			}
			if _, ok := seen[target]; ok {
				continue
			}
			info, err := os.Stat(filepath.Join(project, filepath.FromSlash(target)))
			if err != nil {
				continue
			}
			if !info.IsDir() {
				p.Hooks = append(p.Hooks, fmt.Sprintf("%s: local dependency %s: %s (a tarball; its install scripts run)", manifest, name, spec))
				continue
			}
			dep, ok, err := readPackage(project, target)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			p.scriptHooks(target, dep)
			seen[target] = dep
			more = append(more, target)
		}
	}
	return more, nil
}

// isGitSpec reports whether a dependency spec is a git repository.
func isGitSpec(spec string) bool {
	if slices.ContainsFunc(gitPrefixes, func(pre string) bool { return strings.HasPrefix(spec, pre) }) {
		return true
	}
	if base, _, _ := strings.Cut(spec, "#"); strings.Contains(spec, "://") && strings.HasSuffix(base, ".git") {
		return true
	}
	return githubShorthand.MatchString(spec)
}

// readPackage reads the package.json in the directory rel.
func readPackage(project, rel string) (packageJSON, bool, error) {
	var pkg packageJSON
	file := path.Join(rel, "package.json")
	data, err := os.ReadFile(filepath.Join(project, filepath.FromSlash(file)))
	if errors.Is(err, fs.ErrNotExist) {
		return pkg, false, nil
	} else if err != nil {
		return pkg, false, err
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return pkg, false, fmt.Errorf("parse %s: %w", file, err)
	}
	return pkg, true, nil
}

// scriptHooks adds the lifecycle scripts of the package in rel.
func (p *Plan) scriptHooks(rel string, pkg packageJSON) {
	for _, name := range lifecycleScripts {
		if cmd, ok := pkg.Scripts[name]; ok {
			p.Hooks = append(p.Hooks, fmt.Sprintf("%s: %s: %s", path.Join(rel, "package.json"), name, cmd))
		}
	}
}

// yarnPath returns the yarnPath of .yarnrc.yml, the yarn release the
// project runs instead of corepack's.
func yarnPath(project string) string {
	data, err := os.ReadFile(filepath.Join(project, ".yarnrc.yml"))
	if err != nil {
		return ""
	}
	var rc struct {
		YarnPath string `yaml:"yarnPath"`
	}
	yaml.Unmarshal(data, &rc)
	return rc.YarnPath
}

// packageManagerField is the packageManager values corepack may install:
// a plain version with an optional hash. corepack also accepts URLs, which
// would download any code into the shared cache.
var packageManagerField = regexp.MustCompile(`^(npm|pnpm|yarn)@[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+sha(1|224|256|384|512)\.[0-9a-f]+)?$`)

// managerFromField reads the packageManager field, e.g. "pnpm@9.1.0+sha...".
func managerFromField(field string) (name, version string, err error) {
	if field == "" {
		return "", "", nil
	}
	if !packageManagerField.MatchString(field) {
		return "", "", fmt.Errorf("package.json: unsupported packageManager %q (want npm, pnpm or yarn with a plain version); set prep.command in %s", field, ConfigFile)
	}
	name, version, _ = strings.Cut(field, "@")
	return name, version, nil
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
	// go mod download runs no project code, so all of it is fetching.
	if exists(project, "go.work") {
		p.Steps = append(p.Steps, Step{Name: "go", Fetch: "go mod download"})
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
		p.Steps = append(p.Steps, Step{Name: "go", Fetch: script})
	}
	return err
}

// walk calls fn for each directory of the project (rel is "" for the root)
// with the names of its entries, skipping skipDirs and symlinks.
func walk(project string, fn func(rel string, names []string)) error {
	return walkSkipping(project, skipDirs, fn)
}

// walkSkipping is walk with its own list of directories to skip.
func walkSkipping(project string, skip []string, fn func(rel string, names []string)) error {
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
			if !e.IsDir() || slices.Contains(skip, e.Name()) {
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

// FetchScript returns the shell script that runs every fetch command in
// order and stops at the first failure, or "" when there is none.
func (p Plan) FetchScript() string {
	return script(p.Steps, func(s Step) string { return s.Fetch })
}

// Script returns the shell script that runs every install command in order
// and stops at the first failure, or "" when there is none.
func (p Plan) Script() string {
	return script(p.Steps, func(s Step) string { return s.Script })
}

func script(steps []Step, cmd func(Step) string) string {
	var b strings.Builder
	for _, s := range steps {
		if c := cmd(s); c != "" {
			fmt.Fprintf(&b, "echo %s\n%s\n", shellQuote("==> "+s.Name+": "+c), c)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "set -e\n" + b.String()
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func exists(project, name string) bool {
	_, err := os.Lstat(filepath.Join(project, name))
	return err == nil
}

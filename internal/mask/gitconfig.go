package mask

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"kota/internal/hostgit"
)

// execKeys are the config keys whose value git runs as a command.
var execKeys = []string{
	"core.fsmonitor", "core.sshcommand", "core.editor", "core.pager",
	"core.askpass", "sequence.editor", "diff.external", "credential.helper",
	"gpg.program",
}

// execSubKeys are the keys of the form section.<name>.key whose value git
// runs as a command, as section.key.
var execSubKeys = []string{
	"filter.clean", "filter.smudge", "filter.process", "diff.textconv",
	"diff.command", "merge.driver", "credential.helper", "gpg.program",
}

// configFile is a git config file, and the work tree that relative paths
// in its commands are taken from ("" when unknown).
type configFile struct {
	path, workTree string
}

// configs protects the files that git config includes from inside the
// project, at every config level and in every git dir, as for
// core.hooksPath. Conditional includes count whether or not their condition
// holds now, since the agent may make it hold. Values that run a project
// file are collected for warnExecRefs.
func (p *planner) configs() error {
	var queue []configFile
	// What git reads for the project: system, global, local and worktree
	// config, and the includes whose conditions hold.
	origins, err := configOrigins(p.project)
	if err != nil {
		return err
	}
	for _, o := range origins {
		queue = append(queue, configFile{o, p.project})
	}
	for _, rel := range p.gitDirs {
		dir := filepath.Join(p.project, filepath.FromSlash(rel))
		wt := p.project
		if rel != ".git" {
			v, err := gitConfig(filepath.Join(dir, "config"), "core.worktree")
			switch {
			case err != nil:
				return err
			case v == "":
				wt = ""
			case filepath.IsAbs(v):
				wt = v
			default:
				wt = filepath.Join(dir, v)
			}
		}
		queue = append(queue, configFile{filepath.Join(dir, "config"), wt}, configFile{filepath.Join(dir, "config.worktree"), wt})
		linked, _ := filepath.Glob(filepath.Join(dir, "worktrees", "*", "config.worktree"))
		for _, f := range linked {
			queue = append(queue, configFile{f, ""})
		}
	}
	seen := map[string]bool{}
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		f.path = filepath.Clean(f.path)
		if seen[f.path] {
			continue
		}
		seen[f.path] = true
		if _, err := os.Stat(f.path); err != nil {
			continue
		}
		includes, err := configEntries(f.path, "--type=path", "--get-regexp", `^include(if\..+)?\.path$`)
		if err != nil {
			return err
		}
		for _, kv := range includes {
			target := kv[1]
			if target == "" {
				continue
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(f.path), target)
			}
			if rel, ok := p.inside(target); ok {
				if err := p.file(rel); err != nil {
					return fmt.Errorf("%s %s: %w", kv[0], kv[1], err)
				}
			}
			queue = append(queue, configFile{target, f.workTree})
		}
		values, err := configEntries(f.path, "--list")
		if err != nil {
			return err
		}
		for _, kv := range values {
			if runsCommand(kv[0], kv[1]) {
				p.execPaths(f, kv[0], kv[1])
			}
		}
	}
	return nil
}

// execPaths collects the project files that the command value of key in f
// names. A path counts when it is the command itself, which the agent could
// create, or an existing file.
func (p *planner) execPaths(f configFile, key, value string) {
	home, _ := os.UserHomeDir()
	for i, tok := range pathTokens(value) {
		var abs string
		switch {
		case tok == "":
			continue
		case strings.HasPrefix(tok, "~/") && home != "":
			abs = filepath.Join(home, tok[2:])
		case filepath.IsAbs(tok):
			abs = tok
		case f.workTree != "":
			abs = filepath.Join(f.workTree, tok)
		default:
			continue
		}
		rel, ok := p.inside(abs)
		if !ok || i > 0 && !exists(abs) {
			continue
		}
		config := f.path
		if r, ok := p.inside(f.path); ok {
			config = r
		}
		ref := execRef{key: key, config: config, file: rel}
		if !slices.Contains(p.execRefs, ref) {
			p.execRefs = append(p.execRefs, ref)
		}
	}
}

// warnExecRefs warns about config values that run a project file the
// sandbox can write, and watches the file for the change report. Call it
// once all mounts are planned.
func (p *planner) warnExecRefs() {
	for _, r := range p.execRefs {
		if p.readOnly(r.file) {
			continue
		}
		if !slices.Contains(p.plan.Watch, r.file) {
			p.plan.Watch = append(p.plan.Watch, r.file)
		}
		p.plan.Warnings = append(p.plan.Warnings, fmt.Sprintf(
			"git config %s in %s runs %s, which stays writable in the sandbox; host git runs it, so review changes to it (they are reported after the session)", r.key, r.config, r.file))
	}
	slices.Sort(p.plan.Watch)
}

// readOnly reports whether a planned mount other than a pin holds rel.
func (p *planner) readOnly(rel string) bool {
	return slices.ContainsFunc(p.plan.Mounts, func(m Mount) bool {
		return !m.Pin && (m.Path == rel || m.Dir && strings.HasPrefix(rel, m.Path+"/"))
	})
}

// runsCommand reports whether git runs the value of key, as listed by git
// config (section and key lowercased), as a command.
func runsCommand(key, value string) bool {
	if slices.Contains(execKeys, key) {
		return true
	}
	section, rest, _ := strings.Cut(key, ".")
	if section == "alias" {
		return strings.HasPrefix(strings.TrimSpace(value), "!")
	}
	i := strings.LastIndex(rest, ".")
	return i >= 0 && slices.Contains(execSubKeys, section+"."+rest[i+1:])
}

// pathTokens returns the words of a command that look like file paths. The
// first element is the command word, or "" when that isn't a path. Words
// with variables or URLs are skipped.
func pathTokens(value string) []string {
	value = strings.TrimPrefix(strings.TrimSpace(value), "!")
	words := strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("\"'=;|&()<>`", r)
	})
	var out []string
	for i, w := range words {
		if strings.Contains(w, "/") && !strings.ContainsAny(w, "$%") && !strings.Contains(w, "://") {
			out = append(out, w)
		} else if i == 0 {
			out = append(out, "")
		}
	}
	return out
}

// configOrigins returns the config files git reads for project, absolute.
func configOrigins(project string) ([]string, error) {
	// On the host: config reads run nothing, and hostgit names the
	// repository, so a planted one can't answer.
	cmd := hostgit.Command(project, "config", "--list", "--show-origin", "-z")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git config --list: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var files []string
	parts := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	for i := 0; i+1 < len(parts); i += 2 {
		f, ok := strings.CutPrefix(parts[i], "file:")
		if !ok {
			continue
		}
		if !filepath.IsAbs(f) {
			f = filepath.Join(project, f)
		}
		if !slices.Contains(files, f) {
			files = append(files, f)
		}
	}
	return files, nil
}

// configEntries runs git config on file alone, without its includes, and
// returns the key and value of each entry. No match returns none.
func configEntries(file string, args ...string) ([][2]string, error) {
	// On the host: one file read alone, from outside any repository.
	cmd := hostgit.ConfigFile(file, append([]string{"-z"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && stderr.Len() == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("git config -f %s: %w: %s", file, err, strings.TrimSpace(stderr.String()))
	}
	var kvs [][2]string
	for _, e := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if e == "" {
			continue
		}
		k, v, _ := strings.Cut(e, "\n")
		kvs = append(kvs, [2]string{k, v})
	}
	return kvs, nil
}

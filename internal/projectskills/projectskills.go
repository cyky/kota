// Package projectskills decides whether a Session's Agent loads the
// project's Project skills, the agent skills in Dir, and whether the agent
// may change them.
//
// The project opts in through the skills key of its ConfigFile. Only the
// user, with --skills-writable, can make the folder writable: otherwise it
// is read-only, so one Session can't plant skills for the next.
package projectskills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"kota/internal/deps"
)

// ConfigFile is the per-project config that holds the skills key.
const ConfigFile = deps.ConfigFile

// Dir is where a project keeps its Project skills, relative to the project
// root: the Agent Skills location opencode and pi both read.
const Dir = ".agents/skills"

// Mode is how a Session treats the project's Project skills.
type Mode int

const (
	// Off: the agent doesn't load them, and the folder is ordinary project
	// files.
	Off Mode = iota
	// ReadOnly: the agent loads them, and the folder is read-only.
	ReadOnly
	// Writable: the agent loads them and may change them.
	Writable
)

// String describes the mode for run, shell and check.
func (m Mode) String() string {
	switch m {
	case ReadOnly:
		return "on, read-only (" + Dir + "; --skills-writable to let the agent edit them)"
	case Writable:
		return "on, writable (" + Dir + "; --skills-writable)"
	}
	return "off"
}

// ConfigError is a ConfigFile Read can't parse: whether the project opts
// in is unknown.
type ConfigError struct{ Err error }

func (e *ConfigError) Error() string { return e.Err.Error() }
func (e *ConfigError) Unwrap() error { return e.Err }

// Read returns the Mode of a Session of project: Off unless its ConfigFile
// sets skills to true, then ReadOnly, or Writable when writable, the
// --skills-writable flag, is set. writable without the opt-in is refused,
// since it would do nothing. A ConfigFile that can't be parsed is a
// *ConfigError.
func Read(project string, writable bool) (Mode, error) {
	on, err := enabled(project)
	switch {
	case err != nil:
		return Off, err
	case !on && writable:
		return Off, fmt.Errorf("--skills-writable needs skills: true in %s", ConfigFile)
	case !on:
		return Off, nil
	case writable:
		return Writable, nil
	}
	return ReadOnly, nil
}

func enabled(project string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(project, ConfigFile))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	on, err := Parse(data)
	if err != nil {
		return false, &ConfigError{err}
	}
	return on, nil
}

// Parse reads the skills key of a ConfigFile's content. Other keys are
// left to their own parsers. An absent or null key is false.
func Parse(data []byte) (bool, error) {
	var file struct {
		Skills yaml.Node `yaml:"skills"`
	}
	if err := yaml.Unmarshal(data, &file); err != nil {
		return false, fmt.Errorf("parse %s: %w", ConfigFile, err)
	}
	node := &file.Skills
	if node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	switch {
	case node.Kind == 0, node.Kind == yaml.ScalarNode && node.Tag == "!!null":
		return false, nil
	case node.Kind == yaml.ScalarNode && node.Tag == "!!bool":
		var on bool
		if err := node.Decode(&on); err != nil {
			return false, fmt.Errorf("%s: skills: %w", ConfigFile, err)
		}
		return on, nil
	}
	return false, fmt.Errorf("%s: skills: want true or false, got %q", ConfigFile, node.Value)
}

// CheckPath refuses a Dir, or a directory on the way to it, that is a
// symlink or not a directory: kota would mount, create or copy the folder
// through it, somewhere else. A missing one is fine.
func CheckPath(project string) error {
	rel := ""
	for _, part := range strings.Split(Dir, "/") {
		rel = path.Join(rel, part)
		info, err := os.Lstat(filepath.Join(project, filepath.FromSlash(rel)))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil
		case err != nil:
			return err
		case info.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("refusing to use %s: %s is a symlink; make it a directory", Dir, rel)
		case !info.IsDir():
			return fmt.Errorf("refusing to use %s: %s is a %s, not a directory", Dir, rel, kind(info.Mode()))
		}
	}
	return nil
}

func kind(m fs.FileMode) string {
	if m.IsRegular() {
		return "file"
	}
	return m.Type().String()
}

// CheckLinks refuses a symlink in Dir whose target isn't inside Dir. The
// agents follow links, and a read-only mount over Dir holds the link but
// not a writable target elsewhere in the project. An absolute target is
// refused too: it is a host path, which the sandbox and a No-history copy
// don't resolve the same way. A missing Dir is fine; CheckPath checks the
// way to it.
func CheckLinks(project string) error {
	root := filepath.Join(project, filepath.FromSlash(Dir))
	if info, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	} else if !info.IsDir() {
		return nil
	}
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink == 0 {
			return err
		}
		rel, _ := filepath.Rel(project, p)
		target, _ := os.Readlink(p)
		// Compared against Dir as resolved, in case the project path itself
		// goes through a link.
		if resolved, err := filepath.EvalSymlinks(p); err == nil && !filepath.IsAbs(target) {
			if inside, _ := filepath.Rel(base, resolved); inside != ".." && !strings.HasPrefix(inside, "../") && !filepath.IsAbs(inside) {
				return nil
			}
		}
		return fmt.Errorf("refusing to protect %s: %s is a symlink to %s, which a read-only mount over %s can't hold in place; replace it with a copy, or with a relative link inside %s. For one run or shell session, --skills-writable leaves %s writable and skips this check",
			Dir, filepath.ToSlash(rel), target, Dir, Dir, Dir)
	})
}

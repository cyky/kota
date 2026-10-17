// Package instructions assembles what kota tells the agent in every
// Session: a working style, built in or the user's override from the host,
// followed by facts about the Sandbox that an override can't drop.
package instructions

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Default is the path of the built-in working style in assets.Agents.
const Default = "agents/instructions.md"

// File is the override's name in kota's config directory.
const File = "instructions.md"

// MaxOverride caps the size of the override kota reads.
const MaxOverride = 64 << 10

// DefaultSource names the built-in working style, for the user.
const DefaultSource = "built-in default"

// ConfigHome is the user's config directory, $XDG_CONFIG_HOME, or
// ~/.config when that isn't an absolute path. getenv is os.Getenv.
func ConfigHome(getenv func(string) string, home string) string {
	config := getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(config) {
		config = filepath.Join(home, ".config")
	}
	return config
}

// OverridePath is where the user's working style is read from.
func OverridePath(getenv func(string) string, home string) string {
	return filepath.Join(ConfigHome(getenv, home), "kota", File)
}

// Style is the working style in effect and where it came from.
type Style struct {
	Text string
	// Source is DefaultSource or "override: <path>".
	Source string
}

// Load returns the override at file when there is one, else builtin. The
// override must be a regular file of at most MaxOverride bytes; anything
// else is an error naming file.
func Load(file string, builtin []byte) (Style, error) {
	info, err := os.Stat(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Style{Text: string(builtin), Source: DefaultSource}, nil
	case err != nil:
		return Style{}, fmt.Errorf("agent instructions: %w", err)
	case !info.Mode().IsRegular():
		return Style{}, fmt.Errorf("agent instructions: %s is not a regular file", file)
	case info.Size() > MaxOverride:
		return Style{}, fmt.Errorf("agent instructions: %s is larger than %d bytes", file, MaxOverride)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return Style{}, fmt.Errorf("agent instructions: %w", err)
	}
	// Checked again: the file may have grown since the Stat.
	if len(data) > MaxOverride {
		return Style{}, fmt.Errorf("agent instructions: %s is larger than %d bytes", file, MaxOverride)
	}
	return Style{Text: string(data), Source: "override: " + file}, nil
}

// sandboxFacts is what every agent is told about the Sandbox.
const sandboxFacts = `# This sandbox

- This session is offline: only the model gateway is reachable.
- The project's dependencies are installed locally. Read their source and docs (` + "`go doc`" + `, the Go module cache, ` + "`node_modules`" + `) instead of guessing an API.
- Nothing can be installed in this session. If you need a package that isn't installed, don't add it to the manifest or lockfile yourself: tell the user which package and version you need and why, and ask them to add it on the host and run ` + "`kota prep`" + `. If you're working unattended, write that request in the ticket and park it.
`

// Note is the file the agent reads: the working style, then the Sandbox
// facts, then engineNote (sandbox.EngineNote) when there is one.
func Note(style Style, engineNote string) string {
	var parts []string
	for _, p := range []string{style.Text, sandboxFacts, engineNote} {
		// An empty override leaves out the working style.
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "\n\n") + "\n"
}

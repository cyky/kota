// Package agent is how a session runs one Agent: opencode or pi. It picks
// the agent, reads the user's Agent overrides, and writes the agent's files
// in the session directory: its config, kota's note and its extensions.
package agent

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"kota/assets"
	"kota/internal/instructions"
	"kota/internal/models"
	"kota/internal/override"
	"kota/internal/sandbox"
)

// Default is the agent a session runs without --agent.
const Default = "opencode"

// providerID is the provider name the gateway is registered under.
const providerID = "kota"

// adapters are the agents kota runs, in the order they are listed.
var adapters = []*adapter{&opencode, &pi}

// adapter is what kota knows about one agent.
type adapter struct {
	name string
	// asks is false for an agent without a permission system of its own.
	// The sandbox is its only boundary, so it always runs in full auto.
	asks bool
	// configs are the in-sandbox paths of the config files render returns,
	// each mounted read-only.
	configs []string
	// scratch are writable per-session directories (sandbox.Inputs.Scratch).
	scratch []string
	env     map[string]string
	command []string
	// overrides are the Agent override files the agent reads.
	overrides []overrideFile
	// overrideEnv, when set, returns environment variables carrying the
	// loaded overrides, for an agent that takes its config inline.
	overrideEnv func(ov map[string]map[string]any) (map[string]string, error)
	// render returns the content of each of configs, in order. keyEnv,
	// when set, names the variable holding the gateway key; skills, when
	// set, is the in-sandbox directory of the Project skills. ov holds the
	// loaded overrides by override.File.Name, without those not found.
	render func(c models.Catalog, auto bool, keyEnv, skills string, ov map[string]map[string]any) ([][]byte, error)
	// notePath is where the agent reads kota's instructions, mounted
	// read-only, with noteScratch around it.
	notePath    string
	noteScratch []string
	// facts are what this agent alone is told, after the Sandbox facts.
	facts []string
	// extensions, when set, is where the agent loads extensions from. The
	// agent's bundled extensions (assets agents/<name>/extensions) are
	// mounted there read-only, as a directory, so none can be added.
	extensions string
}

// overrideFile is an Agent override file with what the usage text says
// about it.
type overrideFile struct {
	override.File
	// over is what the override is merged over.
	over string
	// note, when set, follows the allowed keys.
	note string
	// anyKey says what the override may set when its policy allows any
	// key.
	anyKey string
}

// Agent is one agent with the user's Agent overrides loaded.
type Agent struct {
	ad *adapter
	// overrides holds the loaded overrides by override.File.Name, and
	// found their host paths, in the adapter's order.
	overrides map[string]map[string]any
	found     []string
}

// Load picks the agent called name and reads the user's Agent overrides
// of it from configHome ($XDG_CONFIG_HOME). It writes nothing.
func Load(name, configHome string) (*Agent, error) {
	var ad *adapter
	for _, a := range adapters {
		if a.name == name {
			ad = a
		}
	}
	if ad == nil {
		return nil, fmt.Errorf("unknown agent %q; available: %s", name, strings.Join(Names(), ", "))
	}
	a := &Agent{ad: ad, overrides: map[string]map[string]any{}}
	for _, f := range ad.overrides {
		file := override.Path(configHome, f.Name)
		over, ok, err := override.Load(file, f.Policy)
		if err != nil {
			return nil, err
		}
		if ok {
			a.overrides[f.Name] = over
			a.found = append(a.found, file)
		}
	}
	return a, nil
}

// Names returns the agents kota runs.
func Names() []string {
	var names []string
	for _, a := range adapters {
		names = append(names, a.name)
	}
	return names
}

// Name is the agent's name, also used for its state volume.
func (a *Agent) Name() string { return a.ad.name }

// Overrides are the host paths of the Agent override files found, for the
// user.
func (a *Agent) Overrides() []string { return a.found }

// FullAuto reports whether the agent acts without asking: always for an
// agent without a permission system, otherwise only when auto was
// requested.
func (a *Agent) FullAuto(auto bool) bool { return auto || !a.ad.asks }

// Approval describes, for the startup message, whether the agent asks
// before it acts.
func (a *Agent) Approval(auto bool) string {
	switch {
	case !a.FullAuto(auto):
		return fmt.Sprintf("ask (%s asks before edits and commands; --auto to skip)", a.Name())
	case a.FullAuto(false):
		return fmt.Sprintf("auto (%s has no permission system: it edits files and runs commands without asking, and the sandbox is its only boundary)", a.Name())
	}
	return fmt.Sprintf("auto (%s edits files and runs commands without asking)", a.Name())
}

// Note is what the agent is told besides its own facts.
type Note struct {
	// Style is the working style the note starts with.
	Style instructions.Style
	// Engine is the instruction about the Engine VM, empty for a project
	// without one.
	Engine string
}

// Prepared is the agent's part of a session, with its files in place.
type Prepared struct {
	// ReadOnly are the agent's config files, its note and its extensions,
	// each mounted read-only.
	ReadOnly []sandbox.Mount
	// Scratch are writable per-session directories (sandbox.Inputs.Scratch).
	Scratch []string
	// Env is the agent's environment, with the overrides it takes inline.
	Env map[string]string
	// Command starts the agent.
	Command []string

	agent *Agent
	// configs are the host files behind the config mounts, in the order
	// the adapter renders them.
	configs []string
}

// Prepare writes the agent's files into dir, a new directory it owns: the
// config files, empty until Configure, so each mount source exists; kota's
// note; and the bundled extensions.
func (a *Agent) Prepare(dir string, note Note) (*Prepared, error) {
	ad := a.ad
	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil, err
	}
	p := &Prepared{
		Scratch: slices.Concat(ad.scratch, ad.noteScratch),
		Env:     maps.Clone(ad.env),
		Command: ad.command,
		agent:   a,
	}
	for _, target := range ad.configs {
		file := filepath.Join(dir, path.Base(target))
		if err := os.WriteFile(file, nil, 0o644); err != nil {
			return nil, err
		}
		p.configs = append(p.configs, file)
		p.ReadOnly = append(p.ReadOnly, sandbox.Mount{Source: file, Target: target})
	}
	file := filepath.Join(dir, path.Base(ad.notePath))
	if err := os.WriteFile(file, []byte(instructions.Note(note.Style, ad.facts, note.Engine)), 0o644); err != nil {
		return nil, err
	}
	p.ReadOnly = append(p.ReadOnly, sandbox.Mount{Source: file, Target: ad.notePath})
	if ad.extensions != "" {
		ext := filepath.Join(dir, "extensions")
		if err := writeExtensions(ext, ad.name); err != nil {
			return nil, err
		}
		p.ReadOnly = append(p.ReadOnly, sandbox.Mount{Source: ext, Target: ad.extensions})
	}
	if ad.overrideEnv != nil {
		env, err := ad.overrideEnv(a.overrides)
		if err != nil {
			return nil, err
		}
		maps.Copy(p.Env, env)
	}
	return p, nil
}

// Configure writes the agent's config for the models the gateway offers,
// over the files Prepare created. auto approves edits and commands, for an
// agent that asks; keyEnv, when set, names the variable holding the gateway
// key; skills, when set, is the in-sandbox directory of the Project skills
// the agent loads. Nothing else of the project's config is loaded.
func (p *Prepared) Configure(c models.Catalog, auto bool, keyEnv, skills string) error {
	if p.agent == nil {
		return errors.New("agent config written before Prepare")
	}
	contents, err := p.agent.ad.render(c, auto, keyEnv, skills, p.agent.overrides)
	if err != nil {
		return err
	}
	for i, file := range p.configs {
		if err := os.WriteFile(file, contents[i], 0o644); err != nil {
			return err
		}
	}
	return nil
}

// writeExtensions copies the agent's bundled extensions into dir, a new
// directory. It is mounted as a directory, so none can be added.
func writeExtensions(dir, agent string) error {
	src := "agents/" + agent + "/extensions"
	entries, err := assets.Agents.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		data, err := assets.Agents.ReadFile(src + "/" + e.Name())
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

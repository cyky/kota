// Package agent is how a session runs one Agent: opencode or pi. It picks
// the agent, reads the user's Agent overrides and renders the agent's config
// for the sandbox.
package agent

import (
	"fmt"
	"maps"
	"strings"

	"kota/internal/models"
	"kota/internal/override"
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
	overrides []override.File
	// overrideEnv, when set, returns environment variables carrying the
	// loaded overrides, for an agent that takes its config inline.
	overrideEnv func(ov map[string]map[string]any) (map[string]string, error)
	// render returns the content of each of configs, in order. keyEnv,
	// when set, names the variable holding the gateway key. ov holds the
	// loaded overrides by override.File.Name, without those not found.
	render func(c models.Catalog, auto bool, keyEnv string, ov map[string]map[string]any) ([][]byte, error)
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

// Bridge is how cmd/kota wires the agent into a session until Prepare and
// Configure take its place.
type Bridge struct {
	// Configs are the in-sandbox paths of the config files Render returns,
	// each mounted read-only.
	Configs []string
	// Scratch are writable per-session directories (sandbox.Inputs.Scratch).
	Scratch []string
	// NotePath is where the agent reads kota's instructions, mounted
	// read-only, with NoteScratch around it.
	NotePath    string
	NoteScratch []string
	// Facts are what this agent alone is told, after the Sandbox facts.
	Facts []string
	// Extensions, when set, is where the agent's bundled extensions (assets
	// agents/<name>/extensions) are mounted read-only.
	Extensions string
	// Env is the agent's environment, with the overrides it takes inline.
	Env     map[string]string
	Command []string
	// Render returns the content of each of Configs, in order. keyEnv,
	// when set, names the variable holding the gateway key.
	Render func(c models.Catalog, auto bool, keyEnv string) ([][]byte, error)
}

// Bridge returns the agent's session wiring.
func (a *Agent) Bridge() (Bridge, error) {
	ad := a.ad
	env := maps.Clone(ad.env)
	if ad.overrideEnv != nil {
		ovEnv, err := ad.overrideEnv(a.overrides)
		if err != nil {
			return Bridge{}, err
		}
		maps.Copy(env, ovEnv)
	}
	return Bridge{
		Configs:     ad.configs,
		Scratch:     ad.scratch,
		NotePath:    ad.notePath,
		NoteScratch: ad.noteScratch,
		Facts:       ad.facts,
		Extensions:  ad.extensions,
		Env:         env,
		Command:     ad.command,
		Render: func(c models.Catalog, auto bool, keyEnv string) ([][]byte, error) {
			return ad.render(c, auto, keyEnv, a.overrides)
		},
	}, nil
}

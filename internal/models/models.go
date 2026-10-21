// Package models resolves which gateway models an agent is offered: the list
// the gateway reports at launch, merged with the metadata in a models file:
// the built-in models.yaml, or a models section of the user's config.yaml.
// The result is agent-neutral; each agent package renders its own config.
package models

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Model is one model offered to the agent.
type Model struct {
	ID string
	// Context is the context window and Output the maximum output, in
	// tokens.
	Context, Output int
	// Tools reports whether the model handles tool calls.
	Tools bool
}

// Catalog is the resolved model set for a session.
type Catalog struct {
	// Models are the offered models, sorted by ID.
	Models []Model
	// Default is the model the agent starts with.
	Default string
	// Small is the model for titles and summaries; empty means the agent
	// uses Default.
	Small string
	// Notes explain fallbacks, e.g. a small model the gateway doesn't serve.
	Notes []string
}

// File is a parsed models file: models.yaml, or a models section in the
// same format.
type File struct {
	Default string `yaml:"default"`
	Small   string `yaml:"small"`
	// Unknown applies to models not listed in Models, and fills fields a
	// listed model leaves out.
	Unknown Meta            `yaml:"unknown"`
	Models  map[string]Meta `yaml:"models"`
	// Source names where the file came from, for messages, such as
	// "models.yaml".
	Source string `yaml:"-"`
}

// Meta is the metadata for one model. Nil fields fall back to File.Unknown.
type Meta struct {
	Context *int  `yaml:"context"`
	Output  *int  `yaml:"output"`
	Tools   *bool `yaml:"tools"`
	// Agent false hides the model from agents. The default is true.
	Agent *bool `yaml:"agent"`
}

// BuiltinSource is the Source of the built-in models file.
const BuiltinSource = "models.yaml"

// ParseFile parses the built-in models.yaml. Unknown keys are errors, so a
// typo doesn't silently fall back to defaults.
func ParseFile(data []byte) (File, error) {
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return File{}, fmt.Errorf("%s: %w", BuiltinSource, err)
	}
	if err := f.Check(); err != nil {
		return File{}, fmt.Errorf("%s: %w", BuiltinSource, err)
	}
	f.Source = BuiltinSource
	return f, nil
}

// Check reports what a decoded models file lacks to stand on its own: a
// default model, and an unknown entry with context, output and tools.
// Nothing is filled in from another file.
func (f File) Check() error {
	if f.Default == "" {
		return errors.New("default model not set")
	}
	u := f.Unknown
	if u.Context == nil || u.Output == nil || u.Tools == nil {
		return errors.New("unknown needs context, output and tools")
	}
	if u.Agent != nil {
		return errors.New("unknown can't set agent")
	}
	for _, id := range slices.Sorted(maps.Keys(f.Models)) {
		m := f.Models[id]
		for _, v := range []struct {
			name  string
			value *int
		}{{"context", m.Context}, {"output", m.Output}} {
			if v.value != nil && *v.value <= 0 {
				return fmt.Errorf("%s: %s must be positive", id, v.name)
			}
		}
	}
	if *u.Context <= 0 || *u.Output <= 0 {
		return errors.New("unknown context and output must be positive")
	}
	return nil
}

// ParseDiscovered returns the model IDs in an OpenAI-style /v1/models
// response.
func ParseDiscovered(data []byte) ([]string, error) {
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse /v1/models: %w", err)
	}
	var ids []string
	for _, m := range resp.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// Resolve merges the discovered model IDs with f. override, when set,
// replaces f.Default. A default the gateway doesn't serve, or one hidden
// from agents, is an error that lists the available models.
func Resolve(discovered []string, f File, override string) (Catalog, error) {
	var c Catalog
	hidden := map[string]bool{}
	ids := slices.Clone(discovered)
	slices.Sort(ids)
	for _, id := range slices.Compact(ids) {
		m := f.Models[id]
		if m.Agent != nil && !*m.Agent {
			hidden[id] = true
			continue
		}
		c.Models = append(c.Models, Model{
			ID:      id,
			Context: *or(m.Context, f.Unknown.Context),
			Output:  *or(m.Output, f.Unknown.Output),
			Tools:   *or(m.Tools, f.Unknown.Tools),
		})
	}
	if len(c.Models) == 0 {
		return Catalog{}, fmt.Errorf("the gateway serves no models for agents (got %q)", discovered)
	}

	c.Default = f.Default
	what := fmt.Sprintf("default model %q from %s", c.Default, f.Source)
	if override != "" {
		c.Default, what = override, fmt.Sprintf("model %q", override)
	}
	switch {
	case hidden[c.Default]:
		return Catalog{}, fmt.Errorf("%s is hidden from agents (agent: false in %s); available: %s", what, f.Source, c.ids())
	case !c.offers(c.Default):
		return Catalog{}, fmt.Errorf("%s is not served by the gateway; available: %s", what, c.ids())
	}

	switch {
	case f.Small == "":
	case c.offers(f.Small):
		c.Small = f.Small
	default:
		c.Notes = append(c.Notes, fmt.Sprintf("small model %q is not available; titles and summaries use %q", f.Small, c.Default))
	}
	return c, nil
}

func (c Catalog) offers(id string) bool {
	return slices.ContainsFunc(c.Models, func(m Model) bool { return m.ID == id })
}

func (c Catalog) ids() string {
	ids := make([]string, len(c.Models))
	for i, m := range c.Models {
		ids[i] = m.ID
	}
	return strings.Join(ids, ", ")
}

func or[T any](v, fallback *T) *T {
	if v != nil {
		return v
	}
	return fallback
}

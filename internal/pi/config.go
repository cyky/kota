// Package pi renders pi's config for a sandbox session.
//
// pi has no permission system: it edits files and runs commands without
// asking, so the sandbox is its only boundary.
package pi

import (
	"encoding/json"
	"errors"
	"fmt"

	"kota/internal/models"
	"kota/internal/override"
)

// Agent is the agent name, used for its state volume.
const Agent = "pi"

// ProviderID is the provider name the gateway is registered under.
const ProviderID = "kota"

// BaseSettings is the path of the base settings in assets.Agents.
const BaseSettings = "agents/pi/settings.json"

// AppendSystem is the file in pi's agent directory whose content pi adds
// to its system prompt. A project's .pi/APPEND_SYSTEM.md would replace it,
// but pi reads that only in trusted projects, and the base settings trust
// none.
const AppendSystem = "APPEND_SYSTEM.md"

// Keybindings is the file in pi's agent directory that maps action ids to
// keys. pi has no project-level keybindings.
const Keybindings = "keybindings.json"

// KeybindingsOverride is the user's Agent override of keybindings.json.
// Every action may be rebound: the file only names actions and keys, so
// nothing in it can load or run anything.
var KeybindingsOverride = override.File{
	Name:   "pi/" + Keybindings,
	Policy: override.Policy{AnyKey: true, Value: checkBinding},
}

// checkBinding accepts what pi does for one action: a key, or a list of
// keys, empty to disable the action. pi itself silently drops other values.
func checkBinding(v any) error {
	switch b := v.(type) {
	case string:
		return nil
	case []any:
		for _, k := range b {
			if _, ok := k.(string); !ok {
				return errors.New("want a key or a list of keys")
			}
		}
		return nil
	}
	return errors.New("want a key or a list of keys")
}

// RenderKeybindings returns the keybindings.json content for a session:
// the user's override from KeybindingsOverride, or none. pi reads the file
// once at startup and on /reload; it only writes it to migrate legacy
// action names, which it also does in memory when it can't.
func RenderKeybindings(over map[string]any) ([]byte, error) {
	if over == nil {
		over = map[string]any{}
	}
	return json.MarshalIndent(over, "", "  ")
}

// placeholderKey is used without a gateway key: pi only offers a
// provider's models when it has a key, and the gateway doesn't check it.
const placeholderKey = "kota"

// Settings are the inputs for a session's pi config.
type Settings struct {
	// GatewayURL is the gateway base URL as seen from inside the sandbox.
	GatewayURL string
	// Models are the models offered, with the default model. pi has no
	// small model; it uses the session's model for summaries.
	Models models.Catalog
	// APIKeyEnv, when set, names the environment variable holding the
	// gateway key. The config refers to it, so the key isn't written out.
	APIKeyEnv string
}

// RenderSettings returns the settings.json content for a session: base (the
// versioned defaults, including project trust) with the default provider
// and model set on top.
func RenderSettings(base []byte, s Settings) ([]byte, error) {
	cfg := map[string]any{}
	if err := json.Unmarshal(base, &cfg); err != nil {
		return nil, fmt.Errorf("base pi settings: %w", err)
	}
	cfg["defaultProvider"] = ProviderID
	cfg["defaultModel"] = s.Models.Default
	return json.MarshalIndent(cfg, "", "  ")
}

// RenderModels returns the models.json content for a session: the gateway
// as the only custom provider, with the offered models. pi has no per-model
// tool-call flag, so models without tool support are offered too.
func RenderModels(s Settings) ([]byte, error) {
	offered := make([]map[string]any, 0, len(s.Models.Models))
	for _, m := range s.Models.Models {
		offered = append(offered, map[string]any{
			"id":            m.ID,
			"name":          m.ID,
			"contextWindow": m.Context,
			"maxTokens":     m.Output,
		})
	}
	key := placeholderKey
	if s.APIKeyEnv != "" {
		key = "$" + s.APIKeyEnv
	}
	return json.MarshalIndent(map[string]any{
		"providers": map[string]any{
			ProviderID: map[string]any{
				"name":    "kota gateway",
				"baseUrl": s.GatewayURL + "/v1",
				"api":     "openai-completions",
				"apiKey":  key,
				"models":  offered,
			},
		},
	}, "", "  ")
}

// Env returns environment variables that keep pi offline and point it at
// agentDir, which holds the rendered settings.json and models.json, and at
// sessionDir for its sessions.
//
// sessionDir outranks a sessionDir setting, which pi reads from the
// project's .pi/settings.json even when the project isn't trusted.
func Env(agentDir, sessionDir string) map[string]string {
	return map[string]string{
		"PI_CODING_AGENT_DIR":         agentDir,
		"PI_CODING_AGENT_SESSION_DIR": sessionDir,
		"PI_OFFLINE":                  "1",
		"PI_SKIP_VERSION_CHECK":       "1",
		"PI_TELEMETRY":                "0",
	}
}

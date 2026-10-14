// Package opencode renders opencode's config for a sandbox session.
package opencode

import (
	"encoding/json"
	"fmt"

	"localllm/internal/models"
)

// Agent is the agent name, used for its state volume and config directory.
const Agent = "opencode"

// ProviderID is the provider name the gateway is registered under.
const ProviderID = "localllm"

// BaseConfig is the path of the base config in assets.Agents.
const BaseConfig = "agents/opencode/opencode.json"

// autoAllowed are the permissions Auto switches from ask to allow.
var autoAllowed = []string{"edit", "bash"}

// Settings are the inputs for a session's opencode config.
type Settings struct {
	// GatewayURL is the gateway base URL as seen from inside the sandbox.
	GatewayURL string
	// Models are the models offered, with the default and small model.
	Models models.Catalog
	// Auto approves edits and shell commands instead of asking.
	Auto bool
}

// Render returns the opencode.json content for a session: base (the
// versioned permissions and defaults) with the gateway provider, its models
// and the default and small model set on top. With Auto, edits and shell
// commands are allowed; other permissions keep their base values.
func Render(base []byte, s Settings) ([]byte, error) {
	cfg := map[string]any{}
	if err := json.Unmarshal(base, &cfg); err != nil {
		return nil, fmt.Errorf("base opencode config: %w", err)
	}
	offered := map[string]any{}
	for _, m := range s.Models.Models {
		offered[m.ID] = map[string]any{
			"name":      m.ID,
			"tool_call": m.Tools,
			"limit":     map[string]int{"context": m.Context, "output": m.Output},
		}
	}
	cfg["enabled_providers"] = []string{ProviderID}
	cfg["model"] = ProviderID + "/" + s.Models.Default
	delete(cfg, "small_model")
	if s.Models.Small != "" {
		cfg["small_model"] = ProviderID + "/" + s.Models.Small
	}
	if s.Auto {
		perm, _ := cfg["permission"].(map[string]any)
		if perm == nil {
			perm = map[string]any{}
		}
		for _, k := range autoAllowed {
			perm[k] = "allow"
		}
		cfg["permission"] = perm
	}
	cfg["provider"] = map[string]any{
		ProviderID: map[string]any{
			"npm":  "@ai-sdk/openai-compatible",
			"name": "localllm gateway",
			"options": map[string]any{
				"baseURL": s.GatewayURL + "/v1",
			},
			"models": offered,
		},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// Env returns environment variables that keep opencode offline-friendly and
// point it at the rendered config.
//
// The project's opencode.json and .opencode/ are ignored: the agent can
// write them, and they would override configPath. The global config
// directory stays at ~/.config/opencode on the session's tmpfs, because
// opencode writes its service state there; configPath outranks it.
func Env(configPath string) map[string]string {
	return map[string]string{
		"OPENCODE_CONFIG":                 configPath,
		"OPENCODE_DISABLE_PROJECT_CONFIG": "1",
		"OPENCODE_DISABLE_AUTOUPDATE":     "1",
		"OPENCODE_DISABLE_MODELS_FETCH":   "1",
		"OPENCODE_DISABLE_LSP_DOWNLOAD":   "1",
		"OPENCODE_DISABLE_CLAUDE_CODE":    "1",
	}
}

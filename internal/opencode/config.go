// Package opencode renders opencode's config for a sandbox session.
package opencode

import (
	"encoding/json"
	"fmt"
)

// Agent is the agent name, used for its state volume and config directory.
const Agent = "opencode"

// ProviderID is the provider name the gateway is registered under.
const ProviderID = "localllm"

// BaseConfig is the path of the base config in assets.Agents.
const BaseConfig = "agents/opencode/opencode.json"

// Settings are the inputs for a session's opencode config.
type Settings struct {
	// GatewayURL is the gateway base URL as seen from inside the sandbox.
	GatewayURL string
	// Model is the default model ID; it is always offered.
	Model string
}

// Render returns the opencode.json content for a session: base (the
// versioned permissions and defaults) with the gateway provider and model
// set on top.
func Render(base []byte, s Settings) ([]byte, error) {
	cfg := map[string]any{}
	if err := json.Unmarshal(base, &cfg); err != nil {
		return nil, fmt.Errorf("base opencode config: %w", err)
	}
	cfg["enabled_providers"] = []string{ProviderID}
	cfg["model"] = ProviderID + "/" + s.Model
	cfg["provider"] = map[string]any{
		ProviderID: map[string]any{
			"npm":  "@ai-sdk/openai-compatible",
			"name": "localllm gateway",
			"options": map[string]any{
				"baseURL": s.GatewayURL + "/v1",
			},
			"models": map[string]any{
				s.Model: map[string]any{"name": s.Model},
			},
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

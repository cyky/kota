// Package opencode renders opencode's config for a sandbox session.
package opencode

import "encoding/json"

// ProviderID is the provider name the gateway is registered under.
const ProviderID = "localllm"

// Settings are the inputs for a session's opencode config.
type Settings struct {
	// GatewayURL is the gateway base URL as seen from inside the sandbox.
	GatewayURL string
	// Model is the default model ID; it is always offered.
	Model string
}

// Render returns the opencode.json content for a session.
func Render(s Settings) ([]byte, error) {
	cfg := map[string]any{
		"$schema":           "https://opencode.ai/config.json",
		"autoupdate":        false,
		"share":             "disabled",
		"enabled_providers": []string{ProviderID},
		"model":             ProviderID + "/" + s.Model,
		"provider": map[string]any{
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
		},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// Env returns environment variables that keep opencode offline-friendly and
// point it at the rendered config.
func Env(configPath string) map[string]string {
	return map[string]string{
		"OPENCODE_CONFIG":               configPath,
		"OPENCODE_DISABLE_AUTOUPDATE":   "1",
		"OPENCODE_DISABLE_MODELS_FETCH": "1",
		"OPENCODE_DISABLE_LSP_DOWNLOAD": "1",
		"OPENCODE_DISABLE_CLAUDE_CODE":  "1",
	}
}

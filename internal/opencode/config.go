// Package opencode renders opencode's config for a sandbox session.
package opencode

import (
	"encoding/json"
	"fmt"

	"kota/internal/models"
	"kota/internal/override"
)

// Agent is the agent name, used for its state volume and config directory.
const Agent = "opencode"

// ProviderID is the provider name the gateway is registered under.
const ProviderID = "kota"

// BaseConfig is the path of the base config in assets.Agents.
const BaseConfig = "agents/opencode/opencode.json"

// BaseCLI is the path of the base CLI config in assets.Agents: opencode
// v2's terminal preferences (keybinds, theme), kept apart from
// opencode.json.
const BaseCLI = "agents/opencode/cli.json"

// CLIEnv carries the rendered CLI config. opencode merges it over its
// global cli.json, which it writes itself when settings change in the
// TUI, and keeps it authoritative while set. opencode has no project-level
// CLI config.
const CLIEnv = "OPENCODE_CLI_CONFIG_CONTENT"

// CLIOverride is the user's Agent override of cli.json. Its settings only
// change how the terminal UI looks and which keys do what.
var CLIOverride = override.File{
	Name: "opencode/cli.json",
	Policy: override.Policy{
		Allowed: []string{"keybinds", "leader", "theme", "animations", "cursor", "mouse", "scroll", "prompt", "session", "tabs", "diffs", "terminal", "mini", "highlights"},
		Refused: map[string]string{
			"plugins":      "loads code",
			"attention":    "its sounds take file paths, and notifications can't leave the sandbox",
			"debug":        "diagnostics, not a preference",
			"experimental": "unreviewed features",
		},
	},
}

// ConfigOverride is the user's Agent override of opencode.json. Only
// compaction is allowed: the rest is gateway wiring, the approval model,
// code or paths opencode loads, or terminal settings that belong in
// cli.json. opencode 2 still reads v1 names and maps them to v2 ones, so
// both are refused.
var ConfigOverride = override.File{
	Name: "opencode/opencode.json",
	Policy: override.Policy{
		Allowed: []string{"compaction"},
		Refused: map[string]string{
			"plugin":             "loads code",
			"plugins":            "loads code",
			"mcp":                "runs code",
			"lsp":                "runs code",
			"formatter":          "runs code",
			"command":            "runs code",
			"commands":           "runs code",
			"agent":              "runs code",
			"agents":             "runs code",
			"mode":               "runs code",
			"shell":              "runs code",
			"instructions":       "reads paths",
			"watcher":            "reads paths",
			"skills":             "reads paths",
			"reference":          "reads paths",
			"references":         "reads paths",
			"worktree":           "reads paths",
			"provider":           "kota sets the gateway provider",
			"providers":          "kota sets the gateway provider",
			"model":              "use --model",
			"small_model":        "kota sets the small model",
			"enabled_providers":  "kota sets the gateway provider",
			"disabled_providers": "kota sets the gateway provider",
			"permission":         "--auto sets what opencode may do without asking",
			"permissions":        "--auto sets what opencode may do without asking",
			"tools":              "--auto sets what opencode may do without asking",
			"policies":           "kota sets the gateway provider",
			"websearch":          "the gateway is the only network route",
			"share":              "sharing stays off",
			"autoshare":          "sharing stays off",
			"enterprise":         "sharing stays off",
			"autoupdate":         "updates stay off",
			"update":             "updates stay off",
			"server":             "kota runs opencode's terminal UI",
			"experimental":       "unreviewed features",
			"keybinds":           "terminal settings go in cli.json next to this file",
			"theme":              "terminal settings go in cli.json next to this file",
		},
	},
}

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
	// APIKeyEnv, when set, names the environment variable holding the
	// gateway key. The config refers to it, so the key isn't written out.
	APIKeyEnv string
	// Override is the user's Agent override of opencode.json, from
	// ConfigOverride, or nil.
	Override map[string]any
}

// Render returns the opencode.json content for a session: base (the
// versioned permissions and defaults), then the user's override, then the
// gateway provider, its models and the default and small model. With Auto,
// edits and shell commands are allowed; other permissions keep their base
// values.
func Render(base []byte, s Settings) ([]byte, error) {
	cfg := map[string]any{}
	if err := json.Unmarshal(base, &cfg); err != nil {
		return nil, fmt.Errorf("base opencode config: %w", err)
	}
	cfg = override.Merge(cfg, s.Override)
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
	options := map[string]any{"baseURL": s.GatewayURL + "/v1"}
	if s.APIKeyEnv != "" {
		options["apiKey"] = "{env:" + s.APIKeyEnv + "}"
	}
	cfg["provider"] = map[string]any{
		ProviderID: map[string]any{
			"npm":     "@ai-sdk/openai-compatible",
			"name":    "kota gateway",
			"options": options,
			"models":  offered,
		},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// RenderCLI returns the CLI config for a session, for CLIEnv: base with
// the user's override, from CLIOverride, merged on top. over may be nil.
func RenderCLI(base []byte, over map[string]any) (string, error) {
	cfg := map[string]any{}
	if err := json.Unmarshal(base, &cfg); err != nil {
		return "", fmt.Errorf("base opencode cli config: %w", err)
	}
	data, err := json.Marshal(override.Merge(cfg, over))
	return string(data), err
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

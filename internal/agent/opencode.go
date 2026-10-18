package agent

import (
	"encoding/json"
	"fmt"
	"path"

	"kota/assets"
	"kota/internal/models"
	"kota/internal/override"
	"kota/internal/sandbox"
)

const (
	opencodeConfigPath = "/etc/kota/opencode.json"
	// opencodeConfigDir is opencode's global config directory. opencode
	// writes its service state there, so it and ~/.config stay writable on
	// their own tmpfs, around the read-only opencodeInstructionsPath.
	opencodeConfigDir = sandbox.Home + "/.config/opencode"
	// opencodeInstructionsPath holds kota's instructions to opencode.
	// opencode reads the global AGENTS.md as
	// well as the project's; its config's instructions setting is ignored.
	opencodeInstructionsPath = opencodeConfigDir + "/AGENTS.md"
)

// opencodeBaseConfig is the path of the base config in assets.Agents.
const opencodeBaseConfig = "agents/opencode/opencode.json"

// opencodeBaseCLI is the path of the base CLI config in assets.Agents:
// opencode v2's terminal preferences (keybinds, theme), kept apart from
// opencode.json.
const opencodeBaseCLI = "agents/opencode/cli.json"

// opencodeCLIEnv carries the rendered CLI config. opencode merges it over
// its global cli.json, which it writes itself when settings change in the
// TUI, and keeps it authoritative while set. opencode has no project-level
// CLI config.
const opencodeCLIEnv = "OPENCODE_CLI_CONFIG_CONTENT"

// opencodeCLIOverride is the user's Agent override of cli.json. Its
// settings only change how the terminal UI looks and which keys do what.
var opencodeCLIOverride = override.File{
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

// opencodeConfigOverride is the user's Agent override of opencode.json.
// Only compaction is allowed: the rest is gateway wiring, the approval
// model, code or paths opencode loads, or terminal settings that belong in
// cli.json. opencode 2 still reads v1 names and maps them to v2 ones, so
// both are refused.
var opencodeConfigOverride = override.File{
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

// opencode asks before edits and commands, unless --auto.
var opencode = adapter{
	name:        "opencode",
	asks:        true,
	configs:     []string{opencodeConfigPath},
	notePath:    opencodeInstructionsPath,
	noteScratch: []string{path.Dir(opencodeConfigDir), opencodeConfigDir},
	env:         opencodeEnv(opencodeConfigPath),
	command:     []string{"opencode"},
	overrides:   []override.File{opencodeConfigOverride, opencodeCLIOverride},
	// opencode writes cli.json when settings change in the TUI, so the CLI
	// config goes inline instead of in a read-only file.
	overrideEnv: func(ov map[string]map[string]any) (map[string]string, error) {
		base, err := assets.Agents.ReadFile(opencodeBaseCLI)
		if err != nil {
			return nil, err
		}
		content, err := renderOpencodeCLI(base, ov[opencodeCLIOverride.Name])
		return map[string]string{opencodeCLIEnv: content}, err
	},
	render: func(c models.Catalog, auto bool, keyEnv string, ov map[string]map[string]any) ([][]byte, error) {
		base, err := assets.Agents.ReadFile(opencodeBaseConfig)
		if err != nil {
			return nil, err
		}
		cfg, err := renderOpencode(base, opencodeSettings{GatewayURL: sandbox.AgentGatewayURL(), Models: c, Auto: auto, APIKeyEnv: keyEnv, Override: ov[opencodeConfigOverride.Name]})
		return [][]byte{cfg}, err
	},
}

// opencodeAutoAllowed are the permissions Auto switches from ask to allow.
var opencodeAutoAllowed = []string{"edit", "bash"}

// opencodeSettings are the inputs for a session's opencode config.
type opencodeSettings struct {
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
	// opencodeConfigOverride, or nil.
	Override map[string]any
}

// renderOpencode returns the opencode.json content for a session: base
// (the versioned permissions and defaults), then the user's override, then
// the gateway provider, its models and the default and small model. With
// Auto, edits and shell commands are allowed; other permissions keep their
// base values.
func renderOpencode(base []byte, s opencodeSettings) ([]byte, error) {
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
	cfg["enabled_providers"] = []string{providerID}
	cfg["model"] = providerID + "/" + s.Models.Default
	delete(cfg, "small_model")
	if s.Models.Small != "" {
		cfg["small_model"] = providerID + "/" + s.Models.Small
	}
	if s.Auto {
		perm, _ := cfg["permission"].(map[string]any)
		if perm == nil {
			perm = map[string]any{}
		}
		for _, k := range opencodeAutoAllowed {
			perm[k] = "allow"
		}
		cfg["permission"] = perm
	}
	options := map[string]any{"baseURL": s.GatewayURL + "/v1"}
	if s.APIKeyEnv != "" {
		options["apiKey"] = "{env:" + s.APIKeyEnv + "}"
	}
	cfg["provider"] = map[string]any{
		providerID: map[string]any{
			"npm":     "@ai-sdk/openai-compatible",
			"name":    "kota gateway",
			"options": options,
			"models":  offered,
		},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// renderOpencodeCLI returns the CLI config for a session, for
// opencodeCLIEnv: base with the user's override, from opencodeCLIOverride,
// merged on top. over may be nil.
func renderOpencodeCLI(base []byte, over map[string]any) (string, error) {
	cfg := map[string]any{}
	if err := json.Unmarshal(base, &cfg); err != nil {
		return "", fmt.Errorf("base opencode cli config: %w", err)
	}
	data, err := json.Marshal(override.Merge(cfg, over))
	return string(data), err
}

// opencodeEnv returns environment variables that keep opencode
// offline-friendly and point it at the rendered config.
//
// The project's opencode.json and .opencode/ are ignored: the agent can
// write them, and they would override configPath. The global config
// directory stays at ~/.config/opencode on the session's tmpfs, because
// opencode writes its service state there; configPath outranks it.
func opencodeEnv(configPath string) map[string]string {
	return map[string]string{
		"OPENCODE_CONFIG":                 configPath,
		"OPENCODE_DISABLE_PROJECT_CONFIG": "1",
		"OPENCODE_DISABLE_AUTOUPDATE":     "1",
		"OPENCODE_DISABLE_MODELS_FETCH":   "1",
		"OPENCODE_DISABLE_LSP_DOWNLOAD":   "1",
		"OPENCODE_DISABLE_CLAUDE_CODE":    "1",
	}
}

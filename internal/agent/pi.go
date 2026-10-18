package agent

import (
	"encoding/json"
	"errors"
	"fmt"

	"kota/assets"
	"kota/internal/models"
	"kota/internal/override"
	"kota/internal/sandbox"
)

const (
	// piAgentDir is pi's config directory. It is writable, because pi
	// creates lock files next to its settings, and discarded after the
	// session; the rendered settings.json, models.json, keybindings.json and
	// APPEND_SYSTEM.md in it, and its extensions directory, are read-only.
	piAgentDir   = sandbox.Home + "/.pi/agent"
	piSessionDir = sandbox.StateDir + "/pi/sessions"
)

// piBaseSettings is the path of the base settings in assets.Agents.
const piBaseSettings = "agents/pi/settings.json"

// piAppendSystem is the file in pi's agent directory whose content pi adds
// to its system prompt. A project's .pi/APPEND_SYSTEM.md would replace it,
// but pi reads that only in trusted projects, and the base settings trust
// none.
const piAppendSystem = "APPEND_SYSTEM.md"

// piKeybindings is the file in pi's agent directory that maps action ids
// to keys. pi has no project-level keybindings.
const piKeybindings = "keybindings.json"

// piKeybindingsOverride is the user's Agent override of keybindings.json.
// Every action may be rebound: the file only names actions and keys, so
// nothing in it can load or run anything.
var piKeybindingsOverride = override.File{
	Name:   "pi/" + piKeybindings,
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

// pi has no permission system: it edits files and runs commands without
// asking, so the sandbox is its only boundary.
var pi = adapter{
	name:      "pi",
	asks:      false,
	configs:   []string{piAgentDir + "/settings.json", piAgentDir + "/models.json", piAgentDir + "/" + piKeybindings},
	scratch:   []string{piAgentDir},
	overrides: []override.File{piSettingsOverride, piKeybindingsOverride},
	notePath:  piAgentDir + "/" + piAppendSystem,
	facts: []string{"After you commit a finished ticket and mark it done, call the ticket_done tool with the ticket and the commit. " +
		"It clears your context, so the next ticket starts fresh; your state is in the tickets and git history."},
	extensions: piAgentDir + "/extensions",
	env:        piEnv(piAgentDir, piSessionDir),
	command:    []string{"pi"},
	render: func(c models.Catalog, _ bool, keyEnv string, ov map[string]map[string]any) ([][]byte, error) {
		base, err := assets.Agents.ReadFile(piBaseSettings)
		if err != nil {
			return nil, err
		}
		s := piSettings{GatewayURL: sandbox.AgentGatewayURL(), Models: c, APIKeyEnv: keyEnv, Override: ov[piSettingsOverride.Name]}
		settings, err := renderPiSettings(base, s)
		if err != nil {
			return nil, err
		}
		modelsCfg, err := renderPiModels(s)
		if err != nil {
			return nil, err
		}
		keys, err := renderPiKeybindings(ov[piKeybindingsOverride.Name])
		return [][]byte{settings, modelsCfg, keys}, err
	},
}

// renderPiKeybindings returns the keybindings.json content for a session:
// the user's override from piKeybindingsOverride, or none. pi reads the
// file once at startup and on /reload; it only writes it to migrate legacy
// action names, which it also does in memory when it can't.
func renderPiKeybindings(over map[string]any) ([]byte, error) {
	if over == nil {
		over = map[string]any{}
	}
	return json.MarshalIndent(over, "", "  ")
}

// placeholderKey is used without a gateway key: pi only offers a
// provider's models when it has a key, and the gateway doesn't check it.
const placeholderKey = "kota"

// piSettings are the inputs for a session's pi config.
type piSettings struct {
	// GatewayURL is the gateway base URL as seen from inside the sandbox.
	GatewayURL string
	// Models are the models offered, with the default model. pi has no
	// small model; it uses the session's model for summaries.
	Models models.Catalog
	// APIKeyEnv, when set, names the environment variable holding the
	// gateway key. The config refers to it, so the key isn't written out.
	APIKeyEnv string
	// Override is the user's Agent override of settings.json, from
	// piSettingsOverride, or nil.
	Override map[string]any
}

// piSettingsOverride is the user's Agent override of settings.json: how pi
// thinks, compacts, retries and looks, but nothing that loads files or
// code, runs commands, changes trust or telemetry, or touches the gateway
// wiring kota sets. A group, such as terminal, is allowed as a whole.
var piSettingsOverride = override.File{
	Name: "pi/settings.json",
	Policy: override.Policy{
		Allowed: []string{
			"defaultThinkingLevel", "modelThinkingLevels", "thinkingBudgets", "hideThinkingBlock",
			"steeringMode", "followUpMode", "doubleEscapeAction", "treeFilterMode",
			"compaction", "branchSummary", "retry",
			"theme", "quietStartup", "tuiMode", "fullscreenExitOutput", "fullscreenScrollbar", "fullscreenCopyOnSelect",
			"fullscreenWheelScrollLines", "editorPaddingX", "outputPad", "autocompleteMaxVisible", "showHardwareCursor",
			"terminal", "images", "markdown",
		},
		Refused: map[string]string{
			"packages":               "loads code",
			"extensions":             "loads code",
			"skills":                 "loads files",
			"prompts":                "loads files",
			"themes":                 "loads files; theme can name a built-in theme",
			"shellPath":              "runs commands",
			"shellCommandPrefix":     "runs commands",
			"npmCommand":             "runs commands",
			"externalEditor":         "runs commands",
			"defaultProjectTrust":    "would let the project's .pi/ load",
			"enableInstallTelemetry": "telemetry stays off",
			"enableAnalytics":        "telemetry stays off",
			"httpProxy":              "the gateway is the only network route",
			"transport":              "the gateway is the only network route",
			"defaultProvider":        "kota sets the gateway provider",
			"defaultModel":           "use --model",
			"enabledModels":          "kota offers the gateway's models",
			"defaultTools":           "kota sets pi's tools",
			"sessionDir":             "sessions live in kota's state volume",
		},
	},
}

// renderPiSettings returns the settings.json content for a session: base
// (the versioned defaults, including project trust), then the user's
// override, then the default provider and model.
func renderPiSettings(base []byte, s piSettings) ([]byte, error) {
	cfg := map[string]any{}
	if err := json.Unmarshal(base, &cfg); err != nil {
		return nil, fmt.Errorf("base pi settings: %w", err)
	}
	cfg = override.Merge(cfg, s.Override)
	cfg["defaultProvider"] = providerID
	cfg["defaultModel"] = s.Models.Default
	return json.MarshalIndent(cfg, "", "  ")
}

// renderPiModels returns the models.json content for a session: the
// gateway as the only custom provider, with the offered models. pi has no
// per-model tool-call flag, so models without tool support are offered
// too.
func renderPiModels(s piSettings) ([]byte, error) {
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
			providerID: map[string]any{
				"name":    "kota gateway",
				"baseUrl": s.GatewayURL + "/v1",
				"api":     "openai-completions",
				"apiKey":  key,
				"models":  offered,
			},
		},
	}, "", "  ")
}

// piEnv returns environment variables that keep pi offline and point it at
// agentDir, which holds the rendered settings.json and models.json, and at
// sessionDir for its sessions.
//
// sessionDir outranks a sessionDir setting, which pi reads from the
// project's .pi/settings.json even when the project isn't trusted.
func piEnv(agentDir, sessionDir string) map[string]string {
	return map[string]string{
		"PI_CODING_AGENT_DIR":         agentDir,
		"PI_CODING_AGENT_SESSION_DIR": sessionDir,
		"PI_OFFLINE":                  "1",
		"PI_SKIP_VERSION_CHECK":       "1",
		"PI_TELEMETRY":                "0",
	}
}

package agent

import (
	"slices"
	"strings"
	"testing"
)

const (
	piSettingsPath    = piAgentDir + "/settings.json"
	piModelsPath      = piAgentDir + "/models.json"
	piKeybindingsPath = piAgentDir + "/keybindings.json"
)

// piSettingsJSON is settings.json after Configure.
func piSettingsJSON(t *testing.T, overrides map[string]string) map[string]any {
	t.Helper()
	var cfg map[string]any
	mountedJSON(t, configure(t, "pi", overrides, false, ""), piSettingsPath, &cfg)
	return cfg
}

func TestPiSettings(t *testing.T) {
	cfg := piSettingsJSON(t, nil)
	if cfg["defaultProvider"] != providerID || cfg["defaultModel"] != "big" {
		t.Errorf("default = %v/%v, want %s/big", cfg["defaultProvider"], cfg["defaultModel"], providerID)
	}
	// The project's .pi/ is writable by the agent; it must not load in a
	// later session.
	if cfg["defaultProjectTrust"] != "never" {
		t.Errorf("defaultProjectTrust = %v, want never", cfg["defaultProjectTrust"])
	}
	if cfg["enableInstallTelemetry"] != false {
		t.Error("install telemetry not disabled")
	}
	if ext, _ := cfg["extensions"].([]any); !slices.Contains(ext, any("-builtin:llama.cpp")) {
		t.Errorf("extensions = %v, want the llama.cpp extension disabled", cfg["extensions"])
	}
}

// TestPiSettingsOverride checks settings.json gets allowed settings from
// the override, without its $schema replacing the base's, and keeps kota's
// model and the base's project trust.
func TestPiSettingsOverride(t *testing.T) {
	cfg := piSettingsJSON(t, map[string]string{
		"pi/settings.json": `{"$schema": "https://pi.dev/schemas/settings.schema.json", "theme": "light", "terminal": {"showImages": false}, "retry": {"maxRetries": 5}}`,
	})
	term, _ := cfg["terminal"].(map[string]any)
	retry, _ := cfg["retry"].(map[string]any)
	if cfg["theme"] != "light" || term["showImages"] != false || retry["maxRetries"] != 5.0 {
		t.Errorf("settings = %v, want the override merged over the base", cfg)
	}
	if cfg["defaultModel"] != "big" || cfg["defaultProvider"] != providerID || cfg["defaultProjectTrust"] != "never" {
		t.Errorf("settings = %v", cfg)
	}
}

func TestPiSettingsOverridePolicy(t *testing.T) {
	for _, k := range []string{"defaultThinkingLevel", "theme", "terminal", "markdown", "compaction", "retry", "steeringMode", "tuiMode"} {
		if err := refused(t, "pi", "pi/settings.json", `{"`+k+`": {}}`); err != nil {
			t.Errorf("%s refused: %v", k, err)
		}
	}
	for _, k := range []string{"packages", "extensions", "skills", "prompts", "themes", "shellPath", "shellCommandPrefix", "npmCommand", "externalEditor",
		"defaultProjectTrust", "enableInstallTelemetry", "enableAnalytics", "httpProxy", "transport",
		"defaultProvider", "defaultModel", "enabledModels", "defaultTools", "sessionDir"} {
		if err := refused(t, "pi", "pi/settings.json", `{"`+k+`": {}}`); err == nil || !strings.Contains(err.Error(), `"`+k+`"`) {
			t.Errorf("%s: err = %v, want it refused", k, err)
		}
	}
	// Everything the bundled base sets stays out of reach.
	for _, k := range baseKeys(t, piBaseSettings) {
		if k != "$schema" && refused(t, "pi", "pi/settings.json", `{"`+k+`": {}}`) == nil {
			t.Errorf("base key %s can be overridden", k)
		}
	}
}

// piModelsRendered is models.json.
type piModelsRendered struct {
	Providers map[string]struct {
		BaseURL string `json:"baseUrl"`
		API     string `json:"api"`
		APIKey  string `json:"apiKey"`
		Models  []struct {
			ID            string `json:"id"`
			ContextWindow int    `json:"contextWindow"`
			MaxTokens     int    `json:"maxTokens"`
		} `json:"models"`
	} `json:"providers"`
}

func TestPiModels(t *testing.T) {
	var cfg piModelsRendered
	mountedJSON(t, configure(t, "pi", nil, false, ""), piModelsPath, &cfg)
	p, ok := cfg.Providers[providerID]
	if !ok || len(cfg.Providers) != 1 {
		t.Fatalf("providers = %v, want only %s", cfg.Providers, providerID)
	}
	if p.BaseURL != gatewayURL || p.API != "openai-completions" {
		t.Errorf("provider = %+v", p)
	}
	type model struct {
		id                 string
		context, maxTokens int
	}
	var got []model
	for _, m := range p.Models {
		got = append(got, model{m.ID, m.ContextWindow, m.MaxTokens})
	}
	// pi has no per-model tool-call flag, so models without tools are
	// offered too.
	want := []model{{"big", 129024, 16384}, {"notools", 8192, 2048}, {"small", 16384, 4096}}
	if !slices.Equal(got, want) {
		t.Errorf("models = %+v, want %+v", got, want)
	}
}

func TestPiModelsGatewayKey(t *testing.T) {
	cases := map[string]string{
		// pi hides a provider's models when it has no key.
		"": placeholderKey,
		// pi interpolates $NAME; the key itself is never written.
		"KOTA_GATEWAY_KEY": "$KOTA_GATEWAY_KEY",
	}
	for keyEnv, want := range cases {
		var cfg piModelsRendered
		mountedJSON(t, configure(t, "pi", nil, false, keyEnv), piModelsPath, &cfg)
		if got := cfg.Providers[providerID].APIKey; got != want {
			t.Errorf("keyEnv %q: apiKey = %q, want %q", keyEnv, got, want)
		}
	}
}

func TestPiEnv(t *testing.T) {
	env := prepare(t, load(t, "pi", nil), Note{}).Env
	want := map[string]string{
		"PI_CODING_AGENT_DIR":         piAgentDir,
		"PI_CODING_AGENT_SESSION_DIR": piSessionDir,
		"PI_OFFLINE":                  "1",
		"PI_SKIP_VERSION_CHECK":       "1",
		"PI_TELEMETRY":                "0",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
}

// TestPiKeybindings checks pi gets an empty keybindings.json without an
// override, and the override's bindings without its $schema with one.
func TestPiKeybindings(t *testing.T) {
	var cfg map[string]any
	mountedJSON(t, configure(t, "pi", nil, false, ""), piKeybindingsPath, &cfg)
	if len(cfg) != 0 {
		t.Errorf("without an override: keybindings.json = %v", cfg)
	}
	mountedJSON(t, configure(t, "pi", map[string]string{
		"pi/keybindings.json": `{"$schema": "https://pi.dev/schemas/keybindings.schema.json", "app.session.new": "ctrl+shift+n", "tui.altScreen.pageUp": []}`,
	}, false, ""), piKeybindingsPath, &cfg)
	if cfg["app.session.new"] != "ctrl+shift+n" || len(cfg) != 2 {
		t.Errorf("keybindings.json = %v, want the override without $schema", cfg)
	}
}

// TestPiKeybindingsOverridePolicy checks any action may be bound to a key
// or a list of keys, and nothing else.
func TestPiKeybindingsOverridePolicy(t *testing.T) {
	for _, ok := range []string{`"ctrl+k"`, `[]`, `["ctrl+k", "alt+k"]`} {
		if err := refused(t, "pi", "pi/keybindings.json", `{"any.action": `+ok+`}`); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{`3`, `true`, `null`, `{}`, `["ctrl+k", 1]`} {
		err := refused(t, "pi", "pi/keybindings.json", `{"app.session.new": `+bad+`}`)
		if err == nil || !strings.Contains(err.Error(), `"app.session.new"`) {
			t.Errorf("%s: err = %v, want it refused naming the action", bad, err)
		}
	}
}

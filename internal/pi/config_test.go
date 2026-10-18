package pi

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"kota/assets"
	"kota/internal/models"
)

func base(t *testing.T) []byte {
	t.Helper()
	data, err := assets.Agents.ReadFile(BaseSettings)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

var catalog = models.Catalog{
	Models: []models.Model{
		{ID: "big", Context: 129024, Output: 16384, Tools: true},
		{ID: "notools", Context: 8192, Output: 2048, Tools: false},
		{ID: "small", Context: 16384, Output: 4096, Tools: true},
	},
	Default: "big",
	Small:   "small",
}

var settings = Settings{GatewayURL: "http://gateway:8080", Models: catalog}

// renderedSettings is the part of settings.json the tests inspect.
type renderedSettings struct {
	DefaultProvider        string   `json:"defaultProvider"`
	DefaultModel           string   `json:"defaultModel"`
	DefaultProjectTrust    string   `json:"defaultProjectTrust"`
	EnableInstallTelemetry *bool    `json:"enableInstallTelemetry"`
	Extensions             []string `json:"extensions"`
}

func TestRenderSettings(t *testing.T) {
	data, err := RenderSettings(base(t), settings)
	if err != nil {
		t.Fatal(err)
	}
	var cfg renderedSettings
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultProvider != ProviderID || cfg.DefaultModel != "big" {
		t.Errorf("default = %s/%s, want %s/big", cfg.DefaultProvider, cfg.DefaultModel, ProviderID)
	}
	// The project's .pi/ is writable by the agent; it must not load in a
	// later session.
	if cfg.DefaultProjectTrust != "never" {
		t.Errorf("defaultProjectTrust = %q, want never", cfg.DefaultProjectTrust)
	}
	if cfg.EnableInstallTelemetry == nil || *cfg.EnableInstallTelemetry {
		t.Error("install telemetry not disabled")
	}
	if !slices.Contains(cfg.Extensions, "-builtin:llama.cpp") {
		t.Errorf("extensions = %q, want the llama.cpp extension disabled", cfg.Extensions)
	}
}

func TestRenderSettingsReplacesBaseDefaults(t *testing.T) {
	b := []byte(`{"defaultProvider": "anthropic", "defaultModel": "claude", "theme": "dark"}`)
	data, err := RenderSettings(b, settings)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["defaultProvider"] != ProviderID || cfg["defaultModel"] != "big" || cfg["theme"] != "dark" {
		t.Errorf("settings = %v", cfg)
	}
}

func TestRenderSettingsOverride(t *testing.T) {
	b := []byte(`{"defaultProjectTrust": "never", "terminal": {"showImages": true, "clearOnShrink": true}}`)
	s := settings
	s.Override = map[string]any{
		"theme":        "light",
		"terminal":     map[string]any{"showImages": false},
		"defaultModel": "other",
		"retry":        map[string]any{"maxRetries": 5.0},
	}
	data, err := RenderSettings(b, s)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	term := cfg["terminal"].(map[string]any)
	if cfg["theme"] != "light" || term["showImages"] != false || term["clearOnShrink"] != true {
		t.Errorf("settings = %v, want the override merged over the base", cfg)
	}
	// kota's own keys win, and the base's protections stay.
	if cfg["defaultModel"] != "big" || cfg["defaultProvider"] != ProviderID || cfg["defaultProjectTrust"] != "never" {
		t.Errorf("settings = %v", cfg)
	}
}

func TestSettingsOverridePolicy(t *testing.T) {
	p := SettingsOverride.Policy
	if SettingsOverride.Name != "pi/settings.json" || p.AnyKey {
		t.Errorf("override = %+v", SettingsOverride)
	}
	for _, k := range []string{"defaultThinkingLevel", "theme", "terminal", "markdown", "compaction", "retry", "steeringMode", "tuiMode"} {
		if !slices.Contains(p.Allowed, k) {
			t.Errorf("%s not allowed", k)
		}
	}
	for _, k := range []string{"packages", "extensions", "skills", "prompts", "themes", "shellPath", "shellCommandPrefix", "npmCommand", "externalEditor",
		"defaultProjectTrust", "enableInstallTelemetry", "enableAnalytics", "httpProxy", "transport",
		"defaultProvider", "defaultModel", "enabledModels", "defaultTools", "sessionDir"} {
		if slices.Contains(p.Allowed, k) || p.Refused[k] == "" {
			t.Errorf("%s not refused with a reason", k)
		}
	}
	// Everything the bundled base sets stays out of reach.
	cfg := map[string]any{}
	if err := json.Unmarshal(base(t), &cfg); err != nil {
		t.Fatal(err)
	}
	for k := range cfg {
		if slices.Contains(p.Allowed, k) {
			t.Errorf("base key %s can be overridden", k)
		}
	}
}

func TestRenderSettingsBadBase(t *testing.T) {
	if _, err := RenderSettings([]byte("{"), settings); err == nil {
		t.Error("no error for invalid base")
	}
}

// renderedModels is models.json.
type renderedModels struct {
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

func TestRenderModels(t *testing.T) {
	data, err := RenderModels(settings)
	if err != nil {
		t.Fatal(err)
	}
	var cfg renderedModels
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("providers = %v, want only %s", cfg.Providers, ProviderID)
	}
	p, ok := cfg.Providers[ProviderID]
	if !ok {
		t.Fatalf("provider %s missing", ProviderID)
	}
	if p.BaseURL != "http://gateway:8080/v1" || p.API != "openai-completions" {
		t.Errorf("provider = %+v", p)
	}
	// pi hides a provider's models when it has no key.
	if p.APIKey == "" {
		t.Error("provider has no API key")
	}
	type model struct {
		id                 string
		context, maxTokens int
	}
	var got []model
	for _, m := range p.Models {
		got = append(got, model{m.ID, m.ContextWindow, m.MaxTokens})
	}
	want := []model{{"big", 129024, 16384}, {"notools", 8192, 2048}, {"small", 16384, 4096}}
	if !slices.Equal(got, want) {
		t.Errorf("models = %+v, want %+v", got, want)
	}
}

func TestEnv(t *testing.T) {
	env := Env("/home/agent/.pi/agent", "/state/pi/sessions")
	want := map[string]string{
		"PI_CODING_AGENT_DIR":         "/home/agent/.pi/agent",
		"PI_CODING_AGENT_SESSION_DIR": "/state/pi/sessions",
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

func TestRenderKeybindings(t *testing.T) {
	data, err := RenderKeybindings(nil)
	if err != nil || strings.TrimSpace(string(data)) != "{}" {
		t.Errorf("without an override: %q, %v", data, err)
	}
	over := map[string]any{"app.session.new": "ctrl+shift+n", "tui.altScreen.pageUp": []any{}}
	data, err = RenderKeybindings(over)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["app.session.new"] != "ctrl+shift+n" || len(cfg) != 2 {
		t.Errorf("keybindings.json = %v", cfg)
	}
}

func TestKeybindingsOverridePolicy(t *testing.T) {
	if KeybindingsOverride.Name != "pi/keybindings.json" || !KeybindingsOverride.Policy.AnyKey {
		t.Errorf("override = %+v", KeybindingsOverride)
	}
	check := KeybindingsOverride.Policy.Value
	for _, ok := range []any{"ctrl+k", []any{}, []any{"ctrl+k", "alt+k"}} {
		if err := check(ok); err != nil {
			t.Errorf("%v refused: %v", ok, err)
		}
	}
	for _, bad := range []any{3.0, true, nil, map[string]any{}, []any{"ctrl+k", 1.0}} {
		if check(bad) == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestRenderModelsGatewayKey(t *testing.T) {
	cases := []struct {
		keyEnv, want string
	}{
		{"", placeholderKey},
		// pi interpolates $NAME; the key itself is never written.
		{"KOTA_GATEWAY_KEY", "$KOTA_GATEWAY_KEY"},
	}
	for _, c := range cases {
		s := settings
		s.APIKeyEnv = c.keyEnv
		data, err := RenderModels(s)
		if err != nil {
			t.Fatal(err)
		}
		var cfg renderedModels
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatal(err)
		}
		if got := cfg.Providers[ProviderID].APIKey; got != c.want {
			t.Errorf("APIKeyEnv %q: apiKey = %q, want %q", c.keyEnv, got, c.want)
		}
	}
}

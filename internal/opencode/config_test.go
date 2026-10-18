package opencode

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"kota/assets"
	"kota/internal/models"
)

func base(t *testing.T) []byte {
	t.Helper()
	data, err := assets.Agents.ReadFile(BaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// rendered is the part of opencode.json the tests inspect.
type rendered struct {
	Model            string            `json:"model"`
	SmallModel       string            `json:"small_model"`
	Autoupdate       bool              `json:"autoupdate"`
	Share            string            `json:"share"`
	Permission       map[string]string `json:"permission"`
	EnabledProviders []string          `json:"enabled_providers"`
	Provider         map[string]struct {
		Options struct {
			BaseURL string  `json:"baseURL"`
			APIKey  *string `json:"apiKey"`
		} `json:"options"`
		Models map[string]struct {
			Name     string `json:"name"`
			ToolCall bool   `json:"tool_call"`
			Limit    struct {
				Context int `json:"context"`
				Output  int `json:"output"`
			} `json:"limit"`
		} `json:"models"`
	} `json:"provider"`
}

func render(t *testing.T, b []byte, c models.Catalog) rendered {
	t.Helper()
	data, err := Render(b, Settings{GatewayURL: "http://gateway:8080", Models: c})
	if err != nil {
		t.Fatal(err)
	}
	var cfg rendered
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

var catalog = models.Catalog{
	Models: []models.Model{
		{ID: "big", Context: 129024, Output: 16384, Tools: true},
		{ID: "small", Context: 16384, Output: 4096, Tools: false},
	},
	Default: "big",
	Small:   "small",
}

func TestRender(t *testing.T) {
	cfg := render(t, base(t), catalog)

	if cfg.Model != "kota/big" || cfg.SmallModel != "kota/small" {
		t.Errorf("model = %q, small_model = %q", cfg.Model, cfg.SmallModel)
	}
	if cfg.Autoupdate || cfg.Share != "disabled" {
		t.Errorf("autoupdate = %v, share = %q", cfg.Autoupdate, cfg.Share)
	}
	if !slices.Equal(cfg.EnabledProviders, []string{ProviderID}) {
		t.Errorf("enabled_providers = %v", cfg.EnabledProviders)
	}
	p, ok := cfg.Provider[ProviderID]
	if !ok || len(cfg.Provider) != 1 {
		t.Fatalf("provider = %v", cfg.Provider)
	}
	if p.Options.BaseURL != "http://gateway:8080/v1" {
		t.Errorf("baseURL = %q", p.Options.BaseURL)
	}
	if ids := slices.Sorted(maps.Keys(p.Models)); !slices.Equal(ids, []string{"big", "small"}) {
		t.Fatalf("models = %v", ids)
	}
	big, small := p.Models["big"], p.Models["small"]
	if big.Name != "big" || !big.ToolCall || big.Limit.Context != 129024 || big.Limit.Output != 16384 {
		t.Errorf("big = %+v", big)
	}
	if small.ToolCall || small.Limit.Context != 16384 || small.Limit.Output != 4096 {
		t.Errorf("small = %+v", small)
	}
}

func TestRenderWithoutSmallModel(t *testing.T) {
	c := catalog
	c.Small = ""
	cfg := render(t, []byte(`{"small_model":"openai/gpt-mini"}`), c)
	if cfg.SmallModel != "" {
		t.Errorf("small_model = %q, want unset", cfg.SmallModel)
	}
}

func TestBaseConfigAsksBeforeEditsAndCommands(t *testing.T) {
	cfg := render(t, base(t), catalog)
	for _, k := range []string{"edit", "bash"} {
		if cfg.Permission[k] != "ask" {
			t.Errorf("permission %s = %q, want ask", k, cfg.Permission[k])
		}
	}
}

func TestRenderOverridesProvidersInBase(t *testing.T) {
	b := []byte(`{"share":"disabled","enabled_providers":["openai"],"model":"openai/gpt","provider":{"openai":{}}}`)
	cfg := render(t, b, catalog)
	if cfg.Share != "disabled" {
		t.Errorf("share = %q, want the base value", cfg.Share)
	}
	if cfg.Model != "kota/big" || !slices.Equal(cfg.EnabledProviders, []string{ProviderID}) {
		t.Errorf("model = %q, enabled_providers = %v", cfg.Model, cfg.EnabledProviders)
	}
	if _, ok := cfg.Provider["openai"]; ok || len(cfg.Provider) != 1 {
		t.Errorf("provider = %v, want only %s", cfg.Provider, ProviderID)
	}
}

func TestRenderRejectsBadBase(t *testing.T) {
	if _, err := Render([]byte("{"), Settings{Models: catalog}); err == nil {
		t.Error("no error for invalid base config")
	}
}

func TestEnvIgnoresProjectConfig(t *testing.T) {
	env := Env("/etc/kota/opencode.json")
	if env["OPENCODE_CONFIG"] != "/etc/kota/opencode.json" || env["OPENCODE_DISABLE_PROJECT_CONFIG"] != "1" {
		t.Errorf("env = %v", env)
	}
}

func TestRenderCLI(t *testing.T) {
	b := []byte(`{"keybinds": {"leader": "ctrl+x", "session.delete": "ctrl+d"}, "theme": {"name": "system"}}`)
	over := map[string]any{"keybinds": map[string]any{"session.delete": "ctrl+backspace"}}
	content, err := RenderCLI(b, over)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(content), &cfg); err != nil {
		t.Fatal(err)
	}
	keys := cfg["keybinds"].(map[string]any)
	if keys["session.delete"] != "ctrl+backspace" || keys["leader"] != "ctrl+x" || cfg["theme"].(map[string]any)["name"] != "system" {
		t.Errorf("cli config = %v", cfg)
	}

	bundled, err := assets.Agents.ReadFile(BaseCLI)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RenderCLI(bundled, nil); err != nil {
		t.Errorf("bundled cli.json without an override: %v", err)
	}
	if _, err := RenderCLI([]byte("{"), nil); err == nil {
		t.Error("no error for invalid base")
	}
}

func TestCLIOverridePolicy(t *testing.T) {
	if CLIOverride.Name != "opencode/cli.json" {
		t.Errorf("name = %q", CLIOverride.Name)
	}
	for _, k := range []string{"keybinds", "leader", "theme", "scroll", "cursor", "mouse"} {
		if !slices.Contains(CLIOverride.Policy.Allowed, k) {
			t.Errorf("%s not allowed", k)
		}
	}
	for _, k := range []string{"plugins", "attention", "debug", "experimental"} {
		if slices.Contains(CLIOverride.Policy.Allowed, k) || CLIOverride.Policy.Refused[k] == "" {
			t.Errorf("%s not refused with a reason", k)
		}
	}
}

func TestRenderOverride(t *testing.T) {
	data, err := Render(base(t), Settings{
		GatewayURL: "http://gateway:8080",
		Models:     catalog,
		Auto:       true,
		Override: map[string]any{
			"compaction": map[string]any{"auto": false, "keep": map[string]any{"tokens": 4000.0}},
			"model":      "other/model",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	compaction := cfg["compaction"].(map[string]any)
	if compaction["auto"] != false || compaction["keep"].(map[string]any)["tokens"] != 4000.0 {
		t.Errorf("compaction = %v, want the override's", compaction)
	}
	// kota's own keys win, --auto still applies, and the base's settings
	// stay.
	perm := cfg["permission"].(map[string]any)
	if cfg["model"] != ProviderID+"/big" || perm["edit"] != "allow" || perm["bash"] != "allow" || perm["webfetch"] != "deny" ||
		cfg["share"] != "disabled" || cfg["autoupdate"] != false {
		t.Errorf("config = %v", cfg)
	}
}

func TestConfigOverridePolicy(t *testing.T) {
	p := ConfigOverride.Policy
	if ConfigOverride.Name != "opencode/opencode.json" || p.AnyKey || !slices.Equal(p.Allowed, []string{"compaction"}) {
		t.Errorf("override = %+v", ConfigOverride)
	}
	// v1 names, which opencode 2 still reads, and their v2 names.
	for _, k := range []string{"plugin", "plugins", "mcp", "lsp", "formatter", "command", "commands", "agent", "agents", "mode", "shell",
		"instructions", "watcher", "skills", "reference", "references", "worktree",
		"provider", "providers", "model", "small_model", "enabled_providers", "disabled_providers", "permission", "permissions", "tools", "policies", "websearch",
		"share", "autoshare", "enterprise", "autoupdate", "update", "server", "experimental", "keybinds", "theme"} {
		if p.Refused[k] == "" {
			t.Errorf("%s not refused with a reason", k)
		}
	}
	for _, k := range []string{"keybinds", "theme"} {
		if !strings.Contains(p.Refused[k], "cli.json next to this file") {
			t.Errorf("%s refusal = %q, want a pointer to cli.json", k, p.Refused[k])
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

func TestRenderPermissions(t *testing.T) {
	cases := []struct {
		name string
		base []byte
		auto bool
		want map[string]string
	}{
		{"ask by default", base(t), false, map[string]string{"edit": "ask", "bash": "ask", "webfetch": "deny"}},
		{"auto allows edits and commands", base(t), true, map[string]string{"edit": "allow", "bash": "allow", "webfetch": "deny"}},
		{"auto without base permissions", []byte(`{}`), true, map[string]string{"edit": "allow", "bash": "allow"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data, err := Render(c.base, Settings{GatewayURL: "http://gateway:8080", Models: catalog, Auto: c.auto})
			if err != nil {
				t.Fatal(err)
			}
			var cfg rendered
			if err := json.Unmarshal(data, &cfg); err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(cfg.Permission, c.want) {
				t.Errorf("permission = %v, want %v", cfg.Permission, c.want)
			}
		})
	}
}

func TestRenderGatewayKey(t *testing.T) {
	cfg := render(t, base(t), catalog)
	if k := cfg.Provider[ProviderID].Options.APIKey; k != nil {
		t.Errorf("apiKey = %q without a gateway key, want unset", *k)
	}

	data, err := Render(base(t), Settings{GatewayURL: "http://gateway:8080", Models: catalog, APIKeyEnv: "KOTA_GATEWAY_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	var withKey rendered
	if err := json.Unmarshal(data, &withKey); err != nil {
		t.Fatal(err)
	}
	// opencode substitutes {env:NAME}; the key itself is never written.
	if k := withKey.Provider[ProviderID].Options.APIKey; k == nil || *k != "{env:KOTA_GATEWAY_KEY}" {
		t.Errorf("apiKey = %v, want {env:KOTA_GATEWAY_KEY}", k)
	}
}

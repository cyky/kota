package opencode

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"localllm/assets"
	"localllm/internal/models"
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

	if cfg.Model != "localllm/big" || cfg.SmallModel != "localllm/small" {
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
	if cfg.Model != "localllm/big" || !slices.Equal(cfg.EnabledProviders, []string{ProviderID}) {
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
	env := Env("/etc/localllm/opencode.json")
	if env["OPENCODE_CONFIG"] != "/etc/localllm/opencode.json" || env["OPENCODE_DISABLE_PROJECT_CONFIG"] != "1" {
		t.Errorf("env = %v", env)
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

	data, err := Render(base(t), Settings{GatewayURL: "http://gateway:8080", Models: catalog, APIKeyEnv: "LOCALLLM_GATEWAY_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	var withKey rendered
	if err := json.Unmarshal(data, &withKey); err != nil {
		t.Fatal(err)
	}
	// opencode substitutes {env:NAME}; the key itself is never written.
	if k := withKey.Provider[ProviderID].Options.APIKey; k == nil || *k != "{env:LOCALLLM_GATEWAY_KEY}" {
		t.Errorf("apiKey = %v, want {env:LOCALLLM_GATEWAY_KEY}", k)
	}
}

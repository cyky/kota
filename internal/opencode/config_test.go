package opencode

import (
	"encoding/json"
	"testing"

	"localllm/assets"
)

func base(t *testing.T) []byte {
	t.Helper()
	data, err := assets.Agents.ReadFile(BaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRenderPointsAtGateway(t *testing.T) {
	data, err := Render(base(t), Settings{GatewayURL: "http://gateway:8080", Model: "qwen3.6-coder"})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Model            string   `json:"model"`
		Autoupdate       bool     `json:"autoupdate"`
		Share            string   `json:"share"`
		EnabledProviders []string `json:"enabled_providers"`
		Provider         map[string]struct {
			Options struct {
				BaseURL string `json:"baseURL"`
			} `json:"options"`
			Models map[string]any `json:"models"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}

	if cfg.Model != "localllm/qwen3.6-coder" {
		t.Errorf("model = %q", cfg.Model)
	}
	if cfg.Autoupdate || cfg.Share != "disabled" {
		t.Errorf("autoupdate = %v, share = %q", cfg.Autoupdate, cfg.Share)
	}
	if len(cfg.EnabledProviders) != 1 || cfg.EnabledProviders[0] != ProviderID {
		t.Errorf("enabled_providers = %v", cfg.EnabledProviders)
	}
	p, ok := cfg.Provider[ProviderID]
	if !ok {
		t.Fatalf("provider %q missing", ProviderID)
	}
	if p.Options.BaseURL != "http://gateway:8080/v1" {
		t.Errorf("baseURL = %q", p.Options.BaseURL)
	}
	if _, ok := p.Models["qwen3.6-coder"]; !ok {
		t.Errorf("models = %v", p.Models)
	}
}

func TestBaseConfigAsksBeforeEditsAndCommands(t *testing.T) {
	data, err := Render(base(t), Settings{GatewayURL: "http://gateway:8080", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Permission map[string]string `json:"permission"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"edit", "bash"} {
		if cfg.Permission[k] != "ask" {
			t.Errorf("permission %s = %q, want ask", k, cfg.Permission[k])
		}
	}
}

func TestRenderOverridesProvidersInBase(t *testing.T) {
	b := []byte(`{"share":"disabled","enabled_providers":["openai"],"model":"openai/gpt","provider":{"openai":{}}}`)
	data, err := Render(b, Settings{GatewayURL: "http://gateway:8080", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Share            string         `json:"share"`
		Model            string         `json:"model"`
		EnabledProviders []string       `json:"enabled_providers"`
		Provider         map[string]any `json:"provider"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Share != "disabled" {
		t.Errorf("share = %q, want the base value", cfg.Share)
	}
	if cfg.Model != "localllm/m" || len(cfg.EnabledProviders) != 1 || cfg.EnabledProviders[0] != ProviderID {
		t.Errorf("model = %q, enabled_providers = %v", cfg.Model, cfg.EnabledProviders)
	}
	if _, ok := cfg.Provider["openai"]; ok || len(cfg.Provider) != 1 {
		t.Errorf("provider = %v, want only %s", cfg.Provider, ProviderID)
	}
}

func TestRenderRejectsBadBase(t *testing.T) {
	if _, err := Render([]byte("{"), Settings{}); err == nil {
		t.Error("no error for invalid base config")
	}
}

func TestEnvIgnoresProjectConfig(t *testing.T) {
	env := Env("/etc/localllm/opencode.json")
	if env["OPENCODE_CONFIG"] != "/etc/localllm/opencode.json" || env["OPENCODE_DISABLE_PROJECT_CONFIG"] != "1" {
		t.Errorf("env = %v", env)
	}
}

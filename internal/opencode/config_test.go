package opencode

import (
	"encoding/json"
	"testing"
)

func TestRenderPointsAtGateway(t *testing.T) {
	data, err := Render(Settings{GatewayURL: "http://gateway:8080", Model: "qwen3.6-coder"})
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

package agent

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
)

// opencodeRendered is the part of opencode.json the tests inspect.
type opencodeRendered struct {
	Model            string            `json:"model"`
	SmallModel       *string           `json:"small_model"`
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

// opencodeConfig is opencode.json after Configure.
func opencodeConfig(t *testing.T, overrides map[string]string, auto bool, keyEnv string) opencodeRendered {
	t.Helper()
	var cfg opencodeRendered
	mountedJSON(t, configure(t, "opencode", overrides, auto, keyEnv), opencodeConfigPath, &cfg)
	return cfg
}

// opencodeCLI is the CLI config opencode gets inline.
func opencodeCLI(t *testing.T, overrides map[string]string) map[string]any {
	t.Helper()
	p := prepare(t, load(t, "opencode", overrides), Note{})
	content, ok := p.Env[opencodeCLIEnv]
	if !ok {
		t.Fatalf("%s not set in %v", opencodeCLIEnv, p.Env)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(content), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestOpencodeConfig(t *testing.T) {
	cfg := opencodeConfig(t, nil, false, "")
	if cfg.Model != "kota/big" || cfg.SmallModel == nil || *cfg.SmallModel != "kota/small" {
		t.Errorf("model = %q, small_model = %v", cfg.Model, cfg.SmallModel)
	}
	if cfg.Autoupdate || cfg.Share != "disabled" {
		t.Errorf("autoupdate = %v, share = %q", cfg.Autoupdate, cfg.Share)
	}
	if !slices.Equal(cfg.EnabledProviders, []string{providerID}) {
		t.Errorf("enabled_providers = %v", cfg.EnabledProviders)
	}
	p, ok := cfg.Provider[providerID]
	if !ok || len(cfg.Provider) != 1 {
		t.Fatalf("provider = %v", cfg.Provider)
	}
	if p.Options.BaseURL != gatewayURL {
		t.Errorf("baseURL = %q, want %q", p.Options.BaseURL, gatewayURL)
	}
	if ids := slices.Sorted(maps.Keys(p.Models)); !slices.Equal(ids, []string{"big", "notools", "small"}) {
		t.Fatalf("models = %v", ids)
	}
	big, notools := p.Models["big"], p.Models["notools"]
	if big.Name != "big" || !big.ToolCall || big.Limit.Context != 129024 || big.Limit.Output != 16384 {
		t.Errorf("big = %+v", big)
	}
	if notools.ToolCall || notools.Limit.Context != 8192 || notools.Limit.Output != 2048 {
		t.Errorf("notools = %+v", notools)
	}
}

func TestOpencodeConfigWithoutSmallModel(t *testing.T) {
	p := prepare(t, load(t, "opencode", nil), Note{})
	c := catalog
	c.Small = ""
	if err := p.Configure(c, false, ""); err != nil {
		t.Fatal(err)
	}
	var cfg opencodeRendered
	mountedJSON(t, p, opencodeConfigPath, &cfg)
	if cfg.SmallModel != nil {
		t.Errorf("small_model = %q, want unset", *cfg.SmallModel)
	}
}

func TestOpencodePermissions(t *testing.T) {
	cases := map[bool]map[string]string{
		false: {"edit": "ask", "bash": "ask", "webfetch": "deny"},
		true:  {"edit": "allow", "bash": "allow", "webfetch": "deny"},
	}
	for auto, want := range cases {
		if got := opencodeConfig(t, nil, auto, "").Permission; !maps.Equal(got, want) {
			t.Errorf("auto %v: permission = %v, want %v", auto, got, want)
		}
	}
}

func TestOpencodeGatewayKey(t *testing.T) {
	if k := opencodeConfig(t, nil, false, "").Provider[providerID].Options.APIKey; k != nil {
		t.Errorf("apiKey = %q without a gateway key, want unset", *k)
	}
	// opencode substitutes {env:NAME}; the key itself is never written.
	if k := opencodeConfig(t, nil, false, "KOTA_GATEWAY_KEY").Provider[providerID].Options.APIKey; k == nil || *k != "{env:KOTA_GATEWAY_KEY}" {
		t.Errorf("apiKey = %v, want {env:KOTA_GATEWAY_KEY}", k)
	}
}

// TestOpencodeEnv checks opencode reads the mounted config and ignores the
// project's.
func TestOpencodeEnv(t *testing.T) {
	env := prepare(t, load(t, "opencode", nil), Note{}).Env
	if env["OPENCODE_CONFIG"] != opencodeConfigPath || env["OPENCODE_DISABLE_PROJECT_CONFIG"] != "1" || env["OPENCODE_DISABLE_AUTOUPDATE"] != "1" {
		t.Errorf("env = %v", env)
	}
}

// TestOpencodeConfigOverride checks opencode.json gets compaction from the
// override and keeps kota's model, the base's sharing and update settings
// and --auto's permissions.
func TestOpencodeConfigOverride(t *testing.T) {
	data := configure(t, "opencode", map[string]string{
		"opencode/opencode.json": `{"$schema": "https://opencode.ai/config.json", "compaction": {"auto": false, "keep": {"tokens": 4000}}}`,
	}, true, "")
	var cfg map[string]any
	mountedJSON(t, data, opencodeConfigPath, &cfg)
	compaction, _ := cfg["compaction"].(map[string]any)
	keep, _ := compaction["keep"].(map[string]any)
	if compaction["auto"] != false || keep["tokens"] != 4000.0 {
		t.Errorf("compaction = %v, want the override's", compaction)
	}
	perm, _ := cfg["permission"].(map[string]any)
	if cfg["model"] != providerID+"/big" || perm["edit"] != "allow" || perm["webfetch"] != "deny" ||
		cfg["share"] != "disabled" || cfg["autoupdate"] != false {
		t.Errorf("opencode.json = %v", cfg)
	}
}

func TestOpencodeConfigOverrideRefused(t *testing.T) {
	// v1 names, which opencode 2 still reads, and their v2 names.
	for _, k := range []string{"plugin", "plugins", "mcp", "lsp", "formatter", "command", "commands", "agent", "agents", "mode", "shell",
		"instructions", "watcher", "skills", "reference", "references", "worktree",
		"provider", "providers", "model", "small_model", "enabled_providers", "disabled_providers", "permission", "permissions", "tools", "policies", "websearch",
		"share", "autoshare", "enterprise", "autoupdate", "update", "server", "experimental", "keybinds", "theme"} {
		if err := refused(t, "opencode", "opencode/opencode.json", `{"`+k+`": {}}`); err == nil || !strings.Contains(err.Error(), `"`+k+`"`) {
			t.Errorf("%s: err = %v, want it refused", k, err)
		}
	}
	for _, k := range []string{"keybinds", "theme"} {
		if err := refused(t, "opencode", "opencode/opencode.json", `{"`+k+`": {}}`); err == nil || !strings.Contains(err.Error(), "cli.json next to this file") {
			t.Errorf("%s: err = %v, want a pointer to cli.json", k, err)
		}
	}
	// Everything the bundled base sets stays out of reach.
	for _, k := range baseKeys(t, opencodeBaseConfig) {
		if k != "$schema" && refused(t, "opencode", "opencode/opencode.json", `{"`+k+`": {}}`) == nil {
			t.Errorf("base key %s can be overridden", k)
		}
	}
}

// TestOpencodeCLIOverride checks opencode gets the bundled cli.json without
// an override, and the override's keybinds without its $schema with one.
func TestOpencodeCLIOverride(t *testing.T) {
	if cfg := opencodeCLI(t, nil); len(cfg) != 0 {
		t.Errorf("without an override: cli config = %v", cfg)
	}
	cfg := opencodeCLI(t, map[string]string{
		"opencode/cli.json": `{"$schema": "https://opencode.ai/v2/cli.json", "keybinds": {"session.delete": "ctrl+backspace"}, "theme": {"name": "system"}}`,
	})
	keys, _ := cfg["keybinds"].(map[string]any)
	theme, _ := cfg["theme"].(map[string]any)
	if keys["session.delete"] != "ctrl+backspace" || theme["name"] != "system" || cfg["$schema"] != nil {
		t.Errorf("cli config = %v, want the override without $schema", cfg)
	}
}

func TestOpencodeCLIOverridePolicy(t *testing.T) {
	for _, k := range []string{"keybinds", "leader", "theme", "scroll", "cursor", "mouse"} {
		if err := refused(t, "opencode", "opencode/cli.json", `{"`+k+`": {}}`); err != nil {
			t.Errorf("%s refused: %v", k, err)
		}
	}
	for k, why := range map[string]string{"plugins": "loads code", "attention": "file paths", "debug": "diagnostics", "experimental": "unreviewed"} {
		if err := refused(t, "opencode", "opencode/cli.json", `{"`+k+`": {}}`); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%s: err = %v, want it refused because %s", k, err, why)
		}
	}
}

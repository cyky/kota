package gatewayconfig

import (
	"strings"
	"testing"
)

const modelsConfig = `defaultGateway: local
models:
  default: mid
  unknown: { context: 8192, output: 2048, tools: true }
  models:
    mid: { context: 65536 }
gateways:
  local:
    url: http://host.docker.internal:8686
  work-api:
    url: http://host.docker.internal:4000
    models:
      default: big-coder
      small: small-chat
      unknown: { context: 32768, output: 8192, tools: true }
      models:
        big-coder: { context: 200000, output: 32000, tools: true }
`

func TestModels(t *testing.T) {
	cases := []struct {
		name, config, flag string
		def                string // nil models when empty
		source             string
	}{
		{name: "gateway's own", config: modelsConfig, flag: "work-api", def: "big-coder", source: "gateway work-api's own"},
		{name: "config defaults", config: modelsConfig, flag: "local", def: "mid", source: "config defaults"},
		{name: "config defaults for a URL", config: modelsConfig, flag: "http://localhost:9000", def: "mid", source: "config defaults"},
		{name: "built-in without config", flag: "http://localhost:9000", source: ModelsBuiltin},
		{name: "built-in without config defaults", config: twoGateways, flag: "work-api", source: ModelsBuiltin},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := configDir(t, c.config)
			got, err := Resolve(dir, c.flag, env(nil))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(got.ModelsSource, c.source) {
				t.Errorf("source = %q, want %q", got.ModelsSource, c.source)
			}
			if c.def == "" {
				if got.Models != nil {
					t.Errorf("models = %+v, want the built-in ones (nil)", got.Models)
				}
				return
			}
			if got.Models == nil || got.Models.Default != c.def {
				t.Fatalf("models = %+v, want default %q", got.Models, c.def)
			}
			if !strings.HasPrefix(got.Models.Source, dir) {
				t.Errorf("models source %q doesn't name the config", got.Models.Source)
			}
		})
	}
}

// TestModelsNotMerged checks a set is used as a whole: a gateway's own
// models get nothing from the config defaults, and the defaults nothing
// from the built-in models.
func TestModelsNotMerged(t *testing.T) {
	got, err := Resolve(configDir(t, modelsConfig), "work-api", env(nil))
	if err != nil {
		t.Fatal(err)
	}
	m := got.Models
	if _, ok := m.Models["mid"]; ok || *m.Unknown.Context != 32768 || m.Small != "small-chat" {
		t.Errorf("work-api's models got the defaults merged in: %+v", m)
	}
	got, err = Resolve(configDir(t, modelsConfig), "local", env(nil))
	if err != nil {
		t.Fatal(err)
	}
	m = got.Models
	if m.Small != "" || len(m.Models) != 1 || m.Models["mid"].Output != nil {
		t.Errorf("default models got something merged in: %+v", m)
	}
}

func TestModelsErrors(t *testing.T) {
	cases := []struct {
		name, config string
		want         []string
	}{
		{name: "gateway without default",
			config: "gateways:\n  local:\n    url: http://localhost:1\n    models:\n      unknown: { context: 1, output: 1, tools: true }\n",
			want:   []string{"gateway local: models: default model not set"}},
		{name: "gateway's unknown incomplete",
			config: "gateways:\n  local:\n    url: http://localhost:1\n    models:\n      default: a\n      unknown: { context: 1 }\n",
			want:   []string{"gateway local: models: unknown needs context, output and tools"}},
		{name: "defaults without unknown",
			config: "models:\n  default: a\n",
			want:   []string{File + ": models: unknown needs"}},
		{name: "typo in a gateway's models",
			config: "gateways:\n  local:\n    url: http://localhost:1\n    models:\n      default: a\n      unknown: { context: 1, output: 1, tools: true }\n      models:\n        a: { contxt: 5 }\n",
			want:   []string{"line 8", "contxt"}},
		{name: "zero output in defaults",
			config: "models:\n  default: a\n  unknown: { context: 1, output: 1, tools: true }\n  models:\n    a: { output: 0 }\n",
			want:   []string{"models: a: output must be positive"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The broken section is checked whichever gateway is chosen.
			_, err := Resolve(configDir(t, c.config), "http://localhost:9000", env(nil))
			if err == nil {
				t.Fatal("no error")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("err = %v, want it to contain %q", err, w)
				}
			}
		})
	}
}

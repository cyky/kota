package gatewayconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kota/internal/sandbox"
)

const twoGateways = `defaultGateway: local
gateways:
  local:
    url: http://host.docker.internal:8686
  work-api:
    url: http://host.docker.internal:4000
`

// configDir returns a temporary kota config dir holding content as
// config.yaml, or no config.yaml when content is empty.
func configDir(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if content != "" {
		if err := os.WriteFile(filepath.Join(dir, File), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestResolve(t *testing.T) {
	cases := []struct {
		name   string
		config string
		flag   string
		env    string
		want   Choice
	}{
		{name: "no config", want: Choice{URL: DefaultURL, Source: SourceBuiltin}},
		{name: "no config, URL flag", flag: "http://localhost:9000",
			want: Choice{URL: "http://localhost:9000", Source: SourceFlag}},
		{name: "no config, URL env", env: "http://localhost:9000",
			want: Choice{URL: "http://localhost:9000", Source: SourceEnv}},
		{name: "defaultGateway", config: twoGateways,
			want: Choice{Name: "local", URL: "http://host.docker.internal:8686", Source: SourceDefault}},
		{name: "env name over defaultGateway", config: twoGateways, env: "work-api",
			want: Choice{Name: "work-api", URL: "http://host.docker.internal:4000", Source: SourceEnv}},
		{name: "flag name over env", config: twoGateways, flag: "local", env: "work-api",
			want: Choice{Name: "local", URL: "http://host.docker.internal:8686", Source: SourceFlag}},
		{name: "flag URL over config", config: twoGateways, flag: "http://localhost:9000",
			want: Choice{URL: "http://localhost:9000", Source: SourceFlag}},
		{name: "no defaultGateway", config: "gateways:\n  local:\n    url: http://localhost:1\n",
			want: Choice{URL: DefaultURL, Source: SourceBuiltin}},
		{name: "empty config", config: "\n", want: Choice{URL: DefaultURL, Source: SourceBuiltin}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Resolve(configDir(t, c.config), c.flag, env(map[string]string{Env: c.env}))
			if err != nil {
				t.Fatal(err)
			}
			gw, err := sandbox.ParseGateway(c.want.URL)
			if err != nil {
				t.Fatal(err)
			}
			c.want.Gateway = gw
			// The key is TestKey's, the models TestModels'.
			got.Key, got.KeySource, got.KeyHint = "", "", ""
			got.Models, got.ModelsSource = nil, ""
			if got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestResolveErrors(t *testing.T) {
	cases := []struct {
		name   string
		config string
		flag   string
		env    string
		want   []string
	}{
		{name: "unknown name", config: twoGateways, flag: "wrok-api",
			want: []string{`unknown gateway "wrok-api" (from --gateway)`, "defined: local, work-api"}},
		{name: "unknown name from env", config: twoGateways, env: "nope",
			want: []string{`"nope" (from KOTA_GATEWAY)`}},
		{name: "name without config", flag: "local",
			want: []string{`unknown gateway "local"`, "no gateways are defined", File}},
		{name: "bad URL flag", flag: "https://example.com", want: []string{"scheme must be http"}},
		{name: "unknown key", config: "defaultGateway: local\ngateway:\n  local: {}\n",
			want: []string{File, "line 2", "gateway"}},
		{name: "unknown gateway key", config: "gateways:\n  local:\n    url: http://localhost:1\n    uri: x\n",
			want: []string{File, "line 4", "uri"}},
		{name: "bad URL in an unused gateway", config: twoGateways + "  broken:\n    url: http://h/v1\n", flag: "local",
			want: []string{"gateway broken", "must not have a path"}},
		{name: "missing url", config: "gateways:\n  local: {}\n", flag: "http://localhost:1",
			want: []string{"gateway local: url not set"}},
		{name: "undefined defaultGateway", config: "defaultGateway: lcoal\ngateways:\n  local:\n    url: http://localhost:1\n",
			flag: "local", want: []string{`defaultGateway "lcoal"`, "defined: local"}},
		{name: "name like a URL", config: "gateways:\n  \"a/b\":\n    url: http://localhost:1\n",
			want: []string{`gateway name "a/b"`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Resolve(configDir(t, c.config), c.flag, env(map[string]string{Env: c.env}))
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

func TestChoiceString(t *testing.T) {
	named := Choice{Name: "work-api", URL: "http://host.docker.internal:4000", Source: SourceFlag}
	if got, want := named.String(), "work-api http://host.docker.internal:4000 (--gateway)"; got != want {
		t.Errorf("named: %q, want %q", got, want)
	}
	url := Choice{URL: DefaultURL, Source: SourceBuiltin}
	if got, want := url.String(), DefaultURL+" (built-in default)"; got != want {
		t.Errorf("URL: %q, want %q", got, want)
	}
}

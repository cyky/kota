package gatewayconfig

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
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
		// network is the Docker network of want's gateway.
		network string
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
		{name: "dockerNetwork", config: "gateways:\n  team:\n    url: http://model-server:4000\n    dockerNetwork: models_internal\n", flag: "team",
			want: Choice{Name: "team", URL: "http://model-server:4000", Source: SourceFlag}, network: "models_internal"},
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
			gw.Network = c.network
			c.want.Gateway = gw
			// The key is TestKey's, the models TestModels'.
			got.Key, got.KeySource, got.KeyHint = "", "", ""
			got.path = ""
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
		{name: "dockerNetwork host", config: networkConfig("host"), want: []string{File, "gateway team", `dockerNetwork "host"`}},
		{name: "dockerNetwork none", config: networkConfig("none"), want: []string{File, "gateway team", `dockerNetwork "none"`}},
		{name: "dockerNetwork bridge", config: networkConfig("bridge"), want: []string{File, "gateway team", `dockerNetwork "bridge"`}},
		{name: "dockerNetwork of a kota session", config: networkConfig("kota-foo-1234abcd_egress"),
			want: []string{File, "gateway team", `dockerNetwork "kota-foo-1234abcd_egress"`, "kota"}},
		{name: "invalid dockerNetwork", config: networkConfig("models net"), want: []string{File, "gateway team", `dockerNetwork "models net"`}},
		{name: "dockerNetwork in an unused gateway", config: twoGateways + "  team:\n    url: http://m:1\n    dockerNetwork: host\n", flag: "local",
			want: []string{"gateway team", `dockerNetwork "host"`}},
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

// A refused network ends with where to look instead; an invalid name
// doesn't.
func TestNetworkRefusalHint(t *testing.T) {
	const hint = "; name the network the gateway's container is on"
	for network, wantHint := range map[string]bool{"host": true, "kota-foo-1234abcd_egress": true, "models net": false} {
		_, err := Resolve(configDir(t, networkConfig(network)), "", env(nil))
		if err == nil {
			t.Fatalf("%s: no error", network)
		}
		if got := strings.HasSuffix(err.Error(), hint); got != wantHint {
			t.Errorf("%s: err = %v, want hint %v", network, err, wantHint)
		}
	}
}

// networkConfig is a config whose default gateway team is on network.
func networkConfig(network string) string {
	return "defaultGateway: team\ngateways:\n  team:\n    url: http://model-server:4000\n    dockerNetwork: \"" + network + "\"\n"
}

func TestChoiceString(t *testing.T) {
	named := Choice{Name: "work-api", URL: "http://host.docker.internal:4000", Source: SourceFlag}
	if got, want := named.String(), "work-api http://host.docker.internal:4000 (--gateway)"; got != want {
		t.Errorf("named: %q, want %q", got, want)
	}
	onNetwork := Choice{Name: "team", URL: "http://model-server:4000", Source: SourceDefault,
		Gateway: sandbox.Gateway{Host: "model-server", Port: 4000, Network: "models_internal"}}
	if got, want := onNetwork.String(), "team http://model-server:4000 on Docker network models_internal (defaultGateway)"; got != want {
		t.Errorf("on a network: %q, want %q", got, want)
	}
	url := Choice{URL: DefaultURL, Source: SourceBuiltin}
	if got, want := url.String(), DefaultURL+" (built-in default)"; got != want {
		t.Errorf("URL: %q, want %q", got, want)
	}
}

func TestCheckNetwork(t *testing.T) {
	dir := configDir(t, networkConfig("models_internal"))
	team, err := Resolve(dir, "", env(nil))
	if err != nil {
		t.Fatal(err)
	}
	var asked []string
	exists := func(ok bool, err error) func(string) (bool, error) {
		return func(name string) (bool, error) {
			asked = append(asked, name)
			return ok, err
		}
	}

	if err := (Choice{Name: "local"}).CheckNetwork(exists(false, nil)); err != nil || len(asked) != 0 {
		t.Errorf("without a network: err = %v, looked up %v", err, asked)
	}
	if err := team.CheckNetwork(exists(true, nil)); err != nil {
		t.Errorf("existing network: %v", err)
	}
	if !slices.Equal(asked, []string{"models_internal"}) {
		t.Errorf("looked up %v, want [models_internal]", asked)
	}
	err = team.CheckNetwork(exists(false, nil))
	if err == nil {
		t.Fatal("missing network: no error")
	}
	for _, w := range []string{filepath.Join(dir, File), "gateway team", `"models_internal" not found`} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("err = %v, want it to contain %q", err, w)
		}
	}
	dockerDown := errors.New("list Docker networks: exit status 1")
	if err := team.CheckNetwork(exists(false, dockerDown)); !errors.Is(err, dockerDown) {
		t.Errorf("lookup failure: err = %v", err)
	}
}

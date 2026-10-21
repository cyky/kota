package gatewayconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// secrets are the key values the tests use; no error may contain one.
var secrets = []string{"sk-env", "sk-from-file", "sk-work", "sk bad", "X-Evil", "X: y"}

func TestKey(t *testing.T) {
	home := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(home, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("good", "sk-from-file\n")
	write("work", " sk-work \n")
	empty := write("empty", " \n")
	spaced := write("spaced", "sk bad")
	headers := write("headers", "sk\r\nX-Evil: 1")
	config := `defaultGateway: local
gateways:
  local:
    url: http://host.docker.internal:8686
  work-api:
    url: http://host.docker.internal:4000
    keyFile: ~/work
  broken:
    url: http://host.docker.internal:4001
    keyFile: ` + filepath.Join(home, "missing") + `
  empty:
    url: http://host.docker.internal:4002
    keyFile: ` + empty + `
  spaced:
    url: http://host.docker.internal:4003
    keyFile: ` + spaced + `
`
	const url = "http://localhost:9000"

	cases := []struct {
		name    string
		flag    string
		env     map[string]string
		key     string
		source  string
		wantErr string
	}{
		{name: "URL, no key", flag: url, source: NoKey},
		{name: "URL, env key", flag: url, env: map[string]string{EnvKey: "sk-env"}, key: "sk-env", source: EnvKey},
		{name: "URL, env key file", flag: url, env: map[string]string{EnvKeyFile: good}, key: "sk-from-file", source: EnvKeyFile + " " + good},
		{name: "URL, both", flag: url, env: map[string]string{EnvKey: "sk-env", EnvKeyFile: good}, wantErr: "both"},
		{name: "URL, missing file", flag: url, env: map[string]string{EnvKeyFile: filepath.Join(home, "nope")}, wantErr: "no such file"},
		{name: "URL, empty file", flag: url, env: map[string]string{EnvKeyFile: empty}, wantErr: "empty"},
		{name: "URL, space in file", flag: url, env: map[string]string{EnvKeyFile: spaced}, wantErr: "printable"},
		{name: "URL, header injection", flag: url, env: map[string]string{EnvKeyFile: headers}, wantErr: "printable"},
		{name: "URL, newline in env", flag: url, env: map[string]string{EnvKey: "sk\nX: y"}, wantErr: "printable"},
		{name: "named, its keyFile", flag: "work-api", key: "sk-work", source: "keyFile ~/work"},
		{name: "named, env ignored", flag: "work-api", env: map[string]string{EnvKey: "sk-env"}, key: "sk-work", source: "keyFile ~/work"},
		{name: "named without keyFile, env ignored", flag: "local",
			env: map[string]string{EnvKey: "sk-env", EnvKeyFile: good}, source: NoKey},
		{name: "default gateway, env ignored", env: map[string]string{EnvKey: "sk-env"}, source: NoKey},
		{name: "other gateways' key files not read", flag: "local", source: NoKey},
		{name: "named, missing key file", flag: "broken", wantErr: "gateway broken"},
		{name: "named, empty key file", flag: "empty", wantErr: "gateway empty: key file"},
		{name: "named, space in key file", flag: "spaced", wantErr: "printable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vars := map[string]string{"HOME": home}
			for k, v := range c.env {
				vars[k] = v
			}
			got, err := Resolve(configDir(t, config), c.flag, env(vars))
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				// Errors may be printed; they must not contain the key.
				for _, s := range secrets {
					if strings.Contains(err.Error(), s) {
						t.Errorf("error %q contains the key", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Key != c.key || got.KeySource != c.source {
				t.Errorf("key = %q from %q, want %q from %q", got.Key, got.KeySource, c.key, c.source)
			}
			if strings.Contains(got.String(), "sk-") || strings.Contains(got.KeySource, "sk-") {
				t.Errorf("shown %q / %q contains the key", got.String(), got.KeySource)
			}
		})
	}
}

func TestKeyFilePath(t *testing.T) {
	cases := []struct {
		keyFile string
		home    string
		wantErr string
	}{
		{keyFile: "keys/work", home: "/home/u", wantErr: "absolute path or one starting with ~/"},
		{keyFile: "./work", home: "/home/u", wantErr: "absolute path"},
		{keyFile: "~work", home: "/home/u", wantErr: "absolute path"},
		{keyFile: "~/work", home: "", wantErr: "HOME"},
	}
	for _, c := range cases {
		config := "gateways:\n  local:\n    url: http://localhost:1\n  work-api:\n    url: http://localhost:2\n    keyFile: " + c.keyFile + "\n"
		// The bad path is in a gateway that isn't chosen.
		_, err := Resolve(configDir(t, config), "local", env(map[string]string{"HOME": c.home}))
		if err == nil || !strings.Contains(err.Error(), c.wantErr) || !strings.Contains(err.Error(), "gateway work-api") {
			t.Errorf("keyFile %q: err = %v, want %q naming the gateway", c.keyFile, err, c.wantErr)
		}
	}
}

func TestKeyHint(t *testing.T) {
	dir := configDir(t, twoGateways)
	named, err := Resolve(dir, "work-api", env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(named.KeyHint, "keyFile for gateway work-api") {
		t.Errorf("named hint = %q", named.KeyHint)
	}
	url, err := Resolve(dir, "http://localhost:9000", env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(url.KeyHint, EnvKey) {
		t.Errorf("URL hint = %q", url.KeyHint)
	}
}

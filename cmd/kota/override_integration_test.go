//go:build integration

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"kota/internal/docker"
	"kota/internal/opencode"
	"kota/internal/sandbox"
)

// TestAgentOverrideInShell opens an opencode shell Session with a CLI
// config override in a test-only XDG_CONFIG_HOME, and checks opencode's
// environment carries it as written, "$" included, and that a cli.json in
// the project doesn't reach that environment.
func TestAgentOverrideInShell(t *testing.T) {
	requireBaseImage(t)
	// HOME stays, since the docker CLI needs it.
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(config, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	file := filepath.Join(config, "kota", "opencode", "cli.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"keybinds": {"session.delete": "ctrl+shift+d", "leader": "$HOME"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(cache, "kota-test-")
	if err != nil {
		t.Fatal(err)
	}
	root, _ = filepath.EvalSymlinks(root)
	t.Cleanup(func() { os.RemoveAll(root) })
	t.Setenv("KOTA_ALLOWED_ROOT", root)
	project := filepath.Join(root, "app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	hostGit(t, project, "init", "-q")
	if err := os.WriteFile(filepath.Join(project, "cli.json"), []byte(`{"keybinds": {"session.delete": "PROJECT"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	sa := sessionArgs{agent: opencode.Agent, gatewayURL: stubModelGateway(t), dir: project, shell: true}
	var out string
	withSession(t, sa, func(*sandbox.Compose) {}, func(sess *session, ds *docker.Session) {
		out, err = ds.Capture("bash", "-c", `printf %s "$`+opencode.CLIEnv+`"`)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	})
	var cfg struct {
		Keybinds map[string]string `json:"keybinds"`
	}
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("%s = %q: %v", opencode.CLIEnv, out, err)
	}
	if cfg.Keybinds["session.delete"] != "ctrl+shift+d" || cfg.Keybinds["leader"] != "$HOME" {
		t.Errorf("%s = %s, want the override as written", opencode.CLIEnv, out)
	}
}

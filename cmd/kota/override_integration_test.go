//go:build integration

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"kota/internal/docker"
	"kota/internal/opencode"
	"kota/internal/pi"
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

// TestPiKeybindingsInShell opens a pi shell Session with a keybindings
// override, and checks pi's own keybindings manager resolves the override's
// binding, keeps defaults for other actions, and that the file can't be
// written, replaced or removed.
func TestPiKeybindingsInShell(t *testing.T) {
	requireBaseImage(t)
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(config, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	file := filepath.Join(config, "kota", "pi", "keybindings.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"app.session.new": "ctrl+shift+n"}`), 0o644); err != nil {
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

	sa := sessionArgs{agent: pi.Agent, gatewayURL: stubModelGateway(t), dir: project, shell: true}
	var out string
	withSession(t, sa, func(*sandbox.Compose) {}, func(sess *session, ds *docker.Session) {
		out, err = ds.Capture("bash", "-c", `exec 2>&1
f="$PI_CODING_AGENT_DIR/keybindings.json"
node --input-type=module -e '
const root = (await import("node:child_process")).execSync("npm root -g").toString().trim();
const { KeybindingsManager } = await import(root + "/@earendil-works/pi-coding-agent/dist/core/keybindings.js");
const c = KeybindingsManager.create(process.env.PI_CODING_AGENT_DIR).getEffectiveConfig();
console.log("new=" + JSON.stringify(c["app.session.new"]));
console.log("left=" + JSON.stringify(c["tui.editor.cursorLeft"]));
'
echo x >> "$f" && echo WROTE
rm -f "$f" && echo REMOVED
mv "$f" "$f.old" && echo MOVED
true`)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	})
	t.Logf("inside:\n%s", out)
	if !strings.Contains(out, `new="ctrl+shift+n"`) && !strings.Contains(out, `new=["ctrl+shift+n"]`) {
		t.Errorf("pi doesn't resolve the override's binding:\n%s", out)
	}
	if !strings.Contains(out, `left=["left","ctrl+b"]`) {
		t.Errorf("pi lost its default bindings:\n%s", out)
	}
	for _, bad := range []string{"WROTE", "REMOVED", "MOVED"} {
		if strings.Contains(out, bad) {
			t.Errorf("the agent could change keybindings.json: %s", bad)
		}
	}
}

// TestPiSettingsInShell opens a pi shell Session with a settings override,
// and checks pi's own settings manager sees the override's settings next to
// kota's model and the base's project trust.
func TestPiSettingsInShell(t *testing.T) {
	requireBaseImage(t)
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(config, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	file := filepath.Join(config, "kota", "pi", "settings.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"$schema": "https://pi.dev/schemas/settings.schema.json", "theme": "light", "defaultThinkingLevel": "high", "terminal": {"showImages": false}}`), 0o644); err != nil {
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

	sa := sessionArgs{agent: pi.Agent, gatewayURL: stubModelGateway(t), dir: project, shell: true}
	var out string
	withSession(t, sa, func(*sandbox.Compose) {}, func(sess *session, ds *docker.Session) {
		out, err = ds.Capture("bash", "-c", `exec 2>&1
node --input-type=module -e '
const root = (await import("node:child_process")).execSync("npm root -g").toString().trim();
const { SettingsManager } = await import(root + "/@earendil-works/pi-coding-agent/dist/core/settings-manager.js");
const s = SettingsManager.create(process.cwd(), process.env.PI_CODING_AGENT_DIR);
console.log("theme=" + s.getTheme());
console.log("thinking=" + s.getDefaultThinkingLevel());
console.log("showImages=" + s.getShowImages());
console.log("provider=" + s.getDefaultProvider());
console.log("trust=" + s.getDefaultProjectTrust());
'`)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	})
	t.Logf("inside:\n%s", out)
	for _, want := range []string{"theme=light", "thinking=high", "showImages=false", "provider=" + pi.ProviderID, "trust=never"} {
		if !strings.Contains(out, want) {
			t.Errorf("pi's settings lack %s:\n%s", want, out)
		}
	}
}

// TestOpencodeConfigInShell opens an opencode shell Session with an
// opencode.json override, and checks opencode's own config loader reads the
// override's compaction next to the base's sharing, update and permission
// settings.
func TestOpencodeConfigInShell(t *testing.T) {
	requireBaseImage(t)
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(config, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	file := filepath.Join(config, "kota", "opencode", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"$schema": "https://opencode.ai/config.json", "compaction": {"auto": false, "keep": {"tokens": 4000}}}`), 0o644); err != nil {
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

	sa := sessionArgs{agent: opencode.Agent, gatewayURL: stubModelGateway(t), dir: project, shell: true}
	var out string
	withSession(t, sa, func(*sandbox.Compose) {}, func(sess *session, ds *docker.Session) {
		out, err = ds.Capture("bash", "-c", `opencode debug config 2>&1`)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	})
	t.Logf("inside:\n%s", out)
	type rule struct {
		Action string `json:"action"`
		Effect string `json:"effect"`
	}
	type source struct {
		Path string `json:"path"`
		Info struct {
			Update      string `json:"update"`
			Share       string `json:"share"`
			Permissions []rule `json:"permissions"`
			Compaction  struct {
				Auto *bool `json:"auto"`
				Keep struct {
					Tokens int `json:"tokens"`
				} `json:"keep"`
			} `json:"compaction"`
		} `json:"info"`
	}
	var sources []source
	if err := json.Unmarshal([]byte(out[strings.Index(out, "["):]), &sources); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	i := slices.IndexFunc(sources, func(s source) bool { return s.Path == opencodeConfigPath })
	if i < 0 {
		t.Fatalf("opencode doesn't read %s:\n%s", opencodeConfigPath, out)
	}
	info := sources[i].Info
	if info.Compaction.Auto == nil || *info.Compaction.Auto || info.Compaction.Keep.Tokens != 4000 {
		t.Errorf("compaction = %+v, want the override's", info.Compaction)
	}
	if info.Update != "disable" || info.Share != "disabled" {
		t.Errorf("update = %q, share = %q, want the base's", info.Update, info.Share)
	}
	if !slices.Contains(info.Permissions, rule{Action: "edit", Effect: "ask"}) {
		t.Errorf("permissions = %+v, want edits to ask", info.Permissions)
	}
}

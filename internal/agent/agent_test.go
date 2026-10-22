package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"kota/assets"
	"kota/internal/instructions"
	"kota/internal/models"
	"kota/internal/sandbox"
)

// catalog is what the gateway offers in these tests.
var catalog = models.Catalog{
	Models: []models.Model{
		{ID: "big", Context: 129024, Output: 16384, Tools: true},
		{ID: "notools", Context: 8192, Output: 2048, Tools: false},
		{ID: "small", Context: 16384, Output: 4096, Tools: true},
	},
	Default: "big",
	Small:   "small",
}

// gatewayURL is the gateway as the agent reaches it, with its API path.
var gatewayURL = sandbox.AgentGatewayURL() + "/v1"

// writeOverride writes an Agent override named name under configHome.
func writeOverride(t *testing.T, configHome, name, content string) string {
	t.Helper()
	file := filepath.Join(configHome, "kota", name)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

// load is Load with the overrides given, by name, in a fresh config home.
func load(t *testing.T, name string, overrides map[string]string) *Agent {
	t.Helper()
	config := t.TempDir()
	for file, content := range overrides {
		writeOverride(t, config, file, content)
	}
	a, err := Load(name, config)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// refused is the error Load gives for an override, or nil.
func refused(t *testing.T, name, file, content string) error {
	t.Helper()
	config := t.TempDir()
	writeOverride(t, config, file, content)
	_, err := Load(name, config)
	return err
}

// prepare is Prepare into a fresh directory with the built-in style.
func prepare(t *testing.T, a *Agent, note Note) *Prepared {
	t.Helper()
	p, err := a.Prepare(filepath.Join(t.TempDir(), "agent"), note)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// configure is load, prepare and Configure: the agent's files as a session
// starts with them.
func configure(t *testing.T, name string, overrides map[string]string, auto bool, keyEnv string) *Prepared {
	t.Helper()
	p := prepare(t, load(t, name, overrides), Note{})
	if err := p.Configure(catalog, auto, keyEnv, ""); err != nil {
		t.Fatal(err)
	}
	return p
}

// mounted returns the content of the file mounted at target.
func mounted(t *testing.T, p *Prepared, target string) []byte {
	t.Helper()
	i := slices.IndexFunc(p.ReadOnly, func(m sandbox.Mount) bool { return m.Target == target })
	if i < 0 {
		t.Fatalf("nothing mounted at %s in %+v", target, p.ReadOnly)
	}
	data, err := os.ReadFile(p.ReadOnly[i].Source)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// mountedJSON decodes the file mounted at target into v.
func mountedJSON(t *testing.T, p *Prepared, target string, v any) {
	t.Helper()
	if err := json.Unmarshal(mounted(t, p, target), v); err != nil {
		t.Fatalf("%s: %v", target, err)
	}
}

// baseKeys are the keys of a bundled config.
func baseKeys(t *testing.T, file string) []string {
	t.Helper()
	data, err := assets.Agents.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range cfg {
		keys = append(keys, k)
	}
	return keys
}

func TestLoadUnknownAgent(t *testing.T) {
	_, err := Load("claude", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), `unknown agent "claude"; available: opencode, pi`) {
		t.Errorf("err = %v", err)
	}
}

func TestLoadWithoutOverrides(t *testing.T) {
	for _, name := range Names() {
		a := load(t, name, nil)
		if a.Name() != name || len(a.Overrides()) != 0 {
			t.Errorf("%s: name = %q, overrides = %v", name, a.Name(), a.Overrides())
		}
	}
}

func TestLoadOverridesInAdapterOrder(t *testing.T) {
	cases := map[string][]string{
		"opencode": {"opencode/opencode.json", "opencode/cli.json"},
		"pi":       {"pi/settings.json", "pi/keybindings.json"},
	}
	for name, files := range cases {
		config := t.TempDir()
		var want []string
		// Written in reverse, found in the adapter's order.
		for _, f := range slices.Backward(files) {
			want = slices.Insert(want, 0, writeOverride(t, config, f, `{}`))
		}
		a, err := Load(name, config)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(a.Overrides(), want) {
			t.Errorf("%s: overrides = %v, want %v", name, a.Overrides(), want)
		}
	}
}

// TestLoadReadsOnlyItsOwnOverrides checks each agent ignores the other's
// override files, even ones it would refuse.
func TestLoadReadsOnlyItsOwnOverrides(t *testing.T) {
	config := t.TempDir()
	writeOverride(t, config, "pi/settings.json", `{"packages": []}`)
	writeOverride(t, config, "pi/keybindings.json", `{"app.session.new": 3}`)
	if a, err := Load("opencode", config); err != nil || len(a.Overrides()) != 0 {
		t.Errorf("opencode read pi's overrides: %v", err)
	}
	config = t.TempDir()
	writeOverride(t, config, "opencode/cli.json", `{"plugins": []}`)
	writeOverride(t, config, "opencode/opencode.json", `{"mcp": {}}`)
	if a, err := Load("pi", config); err != nil || len(a.Overrides()) != 0 {
		t.Errorf("pi read opencode's overrides: %v", err)
	}
}

func TestLoadRefusedOverrideNamesFileKeyAndWhy(t *testing.T) {
	config := t.TempDir()
	file := writeOverride(t, config, "pi/settings.json", `{"extensions": ["/workspace/evil.ts"]}`)
	_, err := Load("pi", config)
	if err == nil || !strings.Contains(err.Error(), file) || !strings.Contains(err.Error(), `"extensions"`) || !strings.Contains(err.Error(), "loads code") {
		t.Errorf("err = %v, want it to name %s, the key and why", err, file)
	}
}

func TestFullAuto(t *testing.T) {
	cases := []struct {
		agent string
		auto  bool
		want  bool
	}{
		{"opencode", false, false},
		{"opencode", true, true},
		{"pi", false, true},
		{"pi", true, true},
	}
	for _, c := range cases {
		if got := load(t, c.agent, nil).FullAuto(c.auto); got != c.want {
			t.Errorf("%s FullAuto(%v) = %v, want %v", c.agent, c.auto, got, c.want)
		}
	}
}

func TestApproval(t *testing.T) {
	cases := []struct {
		agent string
		auto  bool
		want  string
	}{
		{"opencode", false, "ask (opencode asks before edits and commands; --auto to skip)"},
		{"opencode", true, "auto (opencode edits files and runs commands without asking)"},
		{"pi", false, "auto (pi has no permission system: it edits files and runs commands without asking, and the sandbox is its only boundary)"},
		{"pi", true, "auto (pi has no permission system: it edits files and runs commands without asking, and the sandbox is its only boundary)"},
	}
	for _, c := range cases {
		if got := load(t, c.agent, nil).Approval(c.auto); got != c.want {
			t.Errorf("%s Approval(%v) = %q, want %q", c.agent, c.auto, got, c.want)
		}
	}
}

// TestPrepare checks the files Prepare writes: every config file, empty
// until Configure, kota's note and, for pi, its extensions; all in dir and
// mounted read-only.
func TestPrepare(t *testing.T) {
	cases := []struct {
		agent      string
		configs    []string
		note       string
		scratch    []string
		extensions string
		command    string
	}{
		{"opencode", []string{opencodeConfigPath}, opencodeInstructionsPath, []string{sandbox.Home + "/.config", opencodeConfigDir}, "", "opencode"},
		{"pi", []string{piAgentDir + "/settings.json", piAgentDir + "/models.json", piAgentDir + "/keybindings.json"}, piAgentDir + "/APPEND_SYSTEM.md", []string{piAgentDir}, piAgentDir + "/extensions", "pi"},
	}
	for _, c := range cases {
		t.Run(c.agent, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "agent")
			p, err := load(t, c.agent, nil).Prepare(dir, Note{})
			if err != nil {
				t.Fatal(err)
			}
			var targets []string
			for _, m := range p.ReadOnly {
				targets = append(targets, m.Target)
				if filepath.Dir(m.Source) != dir {
					t.Errorf("%s from %s, want it in %s", m.Target, m.Source, dir)
				}
			}
			want := append(slices.Clone(c.configs), c.note)
			if c.extensions != "" {
				want = append(want, c.extensions)
			}
			if !slices.Equal(targets, want) {
				t.Errorf("read-only = %v, want %v", targets, want)
			}
			for _, cfg := range c.configs {
				if data := mounted(t, p, cfg); len(data) != 0 {
					t.Errorf("%s = %q before Configure, want empty", cfg, data)
				}
			}
			if !slices.Equal(p.Scratch, c.scratch) || !slices.Equal(p.Command, []string{c.command}) {
				t.Errorf("scratch = %v, command = %v", p.Scratch, p.Command)
			}
		})
	}
}

// TestPrepareNote checks the note starts with the style, then the Sandbox
// facts and the agent's own, then the Engine VM note; only pi is told about
// ticket_done.
func TestPrepareNote(t *testing.T) {
	notes := map[string]string{"opencode": opencodeInstructionsPath, "pi": piAgentDir + "/APPEND_SYSTEM.md"}
	style := instructions.Style{Text: "# My style\n\nAlways write haiku.\n"}
	for name, target := range notes {
		p := prepare(t, load(t, name, nil), Note{Style: style, Engine: "ENGINE-NOTE"})
		note := string(mounted(t, p, target))
		if !strings.HasPrefix(note, "# My style\n\nAlways write haiku.\n\n# This sandbox\n") || !strings.HasSuffix(note, "ENGINE-NOTE\n") {
			t.Errorf("%s: note =\n%s", name, note)
		}
		if told := strings.Contains(note, "ticket_done"); told != (name == "pi") {
			t.Errorf("%s: told about ticket_done = %v", name, told)
		}
	}
}

func TestPrepareExtensions(t *testing.T) {
	p := prepare(t, load(t, "pi", nil), Note{})
	i := slices.IndexFunc(p.ReadOnly, func(m sandbox.Mount) bool { return m.Target == piAgentDir+"/extensions" })
	if i < 0 {
		t.Fatalf("no extensions in %+v", p.ReadOnly)
	}
	want, _ := assets.Agents.ReadFile("agents/pi/extensions/ticket-done.ts")
	got, err := os.ReadFile(filepath.Join(p.ReadOnly[i].Source, "ticket-done.ts"))
	if err != nil || string(got) != string(want) {
		t.Errorf("ticket-done.ts: %v, equal %v", err, string(got) == string(want))
	}
}

func TestPrepareRefusesExistingDir(t *testing.T) {
	if _, err := load(t, "opencode", nil).Prepare(t.TempDir(), Note{}); err == nil {
		t.Error("prepared into an existing directory")
	}
}

func TestConfigureBeforePrepare(t *testing.T) {
	if err := (&Prepared{}).Configure(catalog, false, "", ""); err == nil {
		t.Error("Configure without Prepare succeeded")
	}
}

// TestConfigureProjectSkills checks each agent's config lists the Project
// skills directory when the session has one, and sets no skills otherwise,
// while project trust stays off.
func TestConfigureProjectSkills(t *testing.T) {
	const dir = "/workspace/.agents/skills"
	configs := map[string]string{"opencode": opencodeConfigPath, "pi": piAgentDir + "/settings.json"}
	for name, target := range configs {
		for _, skills := range []string{"", dir} {
			t.Run(name+" "+skills, func(t *testing.T) {
				p := prepare(t, load(t, name, nil), Note{})
				if err := p.Configure(catalog, false, "", skills); err != nil {
					t.Fatal(err)
				}
				var cfg map[string]any
				mountedJSON(t, p, target, &cfg)
				got, set := cfg["skills"]
				switch {
				case skills == "" && set:
					t.Errorf("skills = %v without Project skills", got)
				case skills != "" && !slices.Equal(toStrings(got), []string{dir}):
					t.Errorf("skills = %v, want [%s]", got, dir)
				}
				if name == "pi" && cfg["defaultProjectTrust"] != "never" {
					t.Errorf("defaultProjectTrust = %v, want never", cfg["defaultProjectTrust"])
				}
			})
		}
	}
	env := load(t, "opencode", nil).ad.env
	if env["OPENCODE_DISABLE_PROJECT_CONFIG"] != "1" {
		t.Errorf("opencode loads the project's config: %v", env)
	}
}

// toStrings is v as a list of strings, or nil if it isn't one.
func toStrings(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, e := range list {
		s, ok := e.(string)
		if !ok {
			return nil
		}
		out = append(out, s)
	}
	return out
}

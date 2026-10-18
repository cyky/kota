package agent

import (
	"os"
	"strings"
	"testing"
)

func TestAgentHelp(t *testing.T) {
	want := "opencode (default) or pi; pi has no permission system and always runs in full auto"
	if got := AgentHelp(); got != want {
		t.Errorf("AgentHelp() = %q, want %q", got, want)
	}
	if got := AutoHelp(); got != "opencode approves edits and commands without asking" {
		t.Errorf("AutoHelp() = %q", got)
	}
}

// TestOverrideHelp checks the usage text names every override file, what
// it is merged over, and every key it allows.
func TestOverrideHelp(t *testing.T) {
	help := OverrideHelp()
	for _, a := range adapters {
		for _, f := range a.overrides {
			for _, want := range append([]string{"$XDG_CONFIG_HOME/kota/" + f.Name + ",", f.over}, f.Policy.Allowed...) {
				if !strings.Contains(help, want) {
					t.Errorf("help lacks %q:\n%s", want, help)
				}
			}
		}
	}
	for _, want := range []string{"(keybinds and theme go in cli.json)", "rebind any of pi's actions"} {
		if !strings.Contains(help, want) {
			t.Errorf("help lacks %q:\n%s", want, help)
		}
	}
}

// TestOverrideKeysDocumented checks the override table in
// docs/agent-setup.md has a row for every override file, naming every key
// it allows, so a key added to a policy can't be left out of the docs.
func TestOverrideKeysDocumented(t *testing.T) {
	data, err := os.ReadFile("../../docs/agent-setup.md")
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if name, ok := strings.CutPrefix(line, "| `"); ok {
			if name, _, ok = strings.Cut(name, "`"); ok {
				rows[name] = line
			}
		}
	}
	for _, a := range adapters {
		for _, f := range a.overrides {
			row, ok := rows[f.Name]
			if !ok {
				t.Errorf("docs/agent-setup.md has no row for %s", f.Name)
				continue
			}
			for _, k := range f.Policy.Allowed {
				if !strings.Contains(row, "`"+k+"`") {
					t.Errorf("docs/agent-setup.md doesn't name %s's allowed key %s", f.Name, k)
				}
			}
		}
	}
}

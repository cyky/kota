package agent

import (
	"fmt"
	"strings"
)

// AgentHelp describes --agent for the usage text: the agents, the default,
// and which never ask before they act.
func AgentHelp() string {
	var names []string
	for _, a := range adapters {
		name := a.name
		if name == Default {
			name += " (default)"
		}
		names = append(names, name)
	}
	text := list(names, "or")
	for _, a := range adapters {
		if !a.asks {
			text += fmt.Sprintf("; %s has no permission system and always runs in full auto", a.name)
		}
	}
	return text
}

// AutoHelp describes what --auto changes: which agents stop asking before
// edits and commands.
func AutoHelp() string {
	var asking []string
	for _, a := range adapters {
		if a.asks {
			asking = append(asking, a.name)
		}
	}
	return list(asking, "and") + " approves edits and commands without asking"
}

// OverrideHelp describes every Agent override file for the usage text:
// where it is, what it is merged over and what it may set.
func OverrideHelp() string {
	var sentences []string
	for _, a := range adapters {
		for _, f := range a.overrides {
			s := fmt.Sprintf("$XDG_CONFIG_HOME/kota/%s, when present, is merged over %s in every session; ", f.Name, f.over)
			if f.Policy.AnyKey {
				s += "it may " + f.anyKey
			} else {
				s += "it may set " + list(f.Policy.Allowed, "and")
			}
			if f.note != "" {
				s += " (" + f.note + ")"
			}
			sentences = append(sentences, s+".")
		}
	}
	return strings.Join(sentences, " ")
}

// list joins items as prose: "a", "a or b", "a, b or c".
func list(items []string, conj string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " " + conj + " " + items[len(items)-1]
}

package sandbox

// The subset of the compose file format localllm generates. Compose accepts
// JSON, so specs are written with encoding/json.

import (
	"encoding/json"
	"strings"
)

type Compose struct {
	Name     string                 `json:"name"`
	Services map[string]Service     `json:"services"`
	Networks map[string]Network     `json:"networks,omitempty"`
	Volumes  map[string]NamedVolume `json:"volumes,omitempty"`
}

type Service struct {
	Image       string            `json:"image"`
	Command     []string          `json:"command,omitempty"`
	User        string            `json:"user,omitempty"`
	WorkingDir  string            `json:"working_dir,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	// PassEnv are variables the container takes from docker compose's own
	// environment. They are written without a value, so secrets never
	// appear in the compose file.
	PassEnv     []string                  `json:"-"`
	Volumes     []Volume                  `json:"volumes,omitempty"`
	Tmpfs       []string                  `json:"tmpfs,omitempty"`
	Networks    map[string]*NetworkAttach `json:"networks,omitempty"`
	NetworkMode string                    `json:"network_mode,omitempty"`
	DependsOn   []string                  `json:"depends_on,omitempty"`
	CapDrop     []string                  `json:"cap_drop,omitempty"`
	CapAdd      []string                  `json:"cap_add,omitempty"`
	SecurityOpt []string                  `json:"security_opt,omitempty"`
	ReadOnly    bool                      `json:"read_only,omitempty"`
	StdinOpen   bool                      `json:"stdin_open,omitempty"`
	Tty         bool                      `json:"tty,omitempty"`
}

// MarshalJSON writes PassEnv into environment as null values, which
// compose resolves from its own environment (unset stays unset).
func (s Service) MarshalJSON() ([]byte, error) {
	type plain Service
	out := struct {
		plain
		Environment map[string]*string `json:"environment,omitempty"`
	}{plain: plain(s)}
	if len(s.Environment)+len(s.PassEnv) > 0 {
		out.Environment = map[string]*string{}
		for k, v := range s.Environment {
			out.Environment[k] = &v
		}
		for _, k := range s.PassEnv {
			out.Environment[k] = nil
		}
	}
	return json.Marshal(out)
}

type Volume struct {
	Type     string `json:"type"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

type NetworkAttach struct {
	Aliases []string `json:"aliases,omitempty"`
}

type Network struct {
	Internal   bool              `json:"internal,omitempty"`
	DriverOpts map[string]string `json:"driver_opts,omitempty"`
}

// NamedVolume is a top-level volume. Name fixes the Docker volume name
// instead of prefixing it with the compose project. External volumes are
// shared across projects and created outside compose.
type NamedVolume struct {
	Name     string `json:"name"`
	External bool   `json:"external,omitempty"`
}

// Literal returns a copy of c to write to a compose file: compose expands
// $VAR in every value from the caller's environment, so each "$" is doubled
// to keep commands, environment values and paths as given. Without this a
// command mentioning $SOME_SECRET would get the host's value.
func (c Compose) Literal() Compose {
	out := c
	out.Name = literal(c.Name)
	out.Services = make(map[string]Service, len(c.Services))
	for name, s := range c.Services {
		s.Image = literal(s.Image)
		s.Command = literalAll(s.Command)
		s.User = literal(s.User)
		s.WorkingDir = literal(s.WorkingDir)
		if s.Environment != nil {
			env := make(map[string]string, len(s.Environment))
			for k, v := range s.Environment {
				env[k] = literal(v)
			}
			s.Environment = env
		}
		s.PassEnv = append([]string(nil), s.PassEnv...)
		vols := make([]Volume, len(s.Volumes))
		for i, v := range s.Volumes {
			v.Source, v.Target = literal(v.Source), literal(v.Target)
			vols[i] = v
		}
		if s.Volumes == nil {
			vols = nil
		}
		s.Volumes = vols
		s.Tmpfs = literalAll(s.Tmpfs)
		out.Services[name] = s
	}
	if c.Volumes != nil {
		out.Volumes = make(map[string]NamedVolume, len(c.Volumes))
		for k, v := range c.Volumes {
			out.Volumes[k] = NamedVolume{Name: literal(v.Name), External: v.External}
		}
	}
	return out
}

func literal(s string) string { return strings.ReplaceAll(s, "$", "$$") }

func literalAll(ss []string) []string {
	if ss == nil {
		return nil
	}
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = literal(s)
	}
	return out
}

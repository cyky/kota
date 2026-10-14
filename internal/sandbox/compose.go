package sandbox

// The subset of the compose file format localllm generates. Compose accepts
// JSON, so specs are written with encoding/json.

type Compose struct {
	Name     string                 `json:"name"`
	Services map[string]Service     `json:"services"`
	Networks map[string]Network     `json:"networks,omitempty"`
	Volumes  map[string]NamedVolume `json:"volumes,omitempty"`
}

type Service struct {
	Image       string                    `json:"image"`
	Command     []string                  `json:"command,omitempty"`
	User        string                    `json:"user,omitempty"`
	WorkingDir  string                    `json:"working_dir,omitempty"`
	Environment map[string]string         `json:"environment,omitempty"`
	Volumes     []Volume                  `json:"volumes,omitempty"`
	Tmpfs       []string                  `json:"tmpfs,omitempty"`
	Networks    map[string]*NetworkAttach `json:"networks,omitempty"`
	DependsOn   []string                  `json:"depends_on,omitempty"`
	CapDrop     []string                  `json:"cap_drop,omitempty"`
	SecurityOpt []string                  `json:"security_opt,omitempty"`
	ReadOnly    bool                      `json:"read_only,omitempty"`
	StdinOpen   bool                      `json:"stdin_open,omitempty"`
	Tty         bool                      `json:"tty,omitempty"`
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
// instead of prefixing it with the compose project.
type NamedVolume struct {
	Name string `json:"name"`
}

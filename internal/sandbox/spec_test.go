package sandbox

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

func testInputs() Inputs {
	return Inputs{
		ProjectDir:     "/Users/me/Projects/foo",
		UID:            501,
		GID:            20,
		Gateway:        Gateway{Host: "host.docker.internal", Port: 8080},
		AgentImage:     "agent:test",
		ForwarderImage: "forwarder:test",
		Pinned:         []Mount{{Source: "/Users/me/Projects/foo/.git", Target: "/workspace/.git"}},
		ReadOnly:       []Mount{{Source: "/tmp/cfg.json", Target: "/etc/localllm/opencode.json"}},
		Env:            map[string]string{"FOO": "bar"},
	}
}

func TestAgentIsOnlyOnInternalNetwork(t *testing.T) {
	spec := Build(testInputs())
	agent := spec.Services[AgentService]

	if len(agent.Networks) != 1 {
		t.Fatalf("agent networks = %v, want exactly one", agent.Networks)
	}
	for name := range agent.Networks {
		net := spec.Networks[name]
		if !net.Internal {
			t.Errorf("agent network %q is not internal", name)
		}
		if net.DriverOpts["com.docker.network.bridge.gateway_mode_ipv4"] != "isolated" {
			t.Errorf("agent network %q has a gateway address on the Docker VM", name)
		}
	}
}

func TestForwarderBridgesToGatewayOnly(t *testing.T) {
	spec := Build(testInputs())
	fwd := spec.Services[ForwarderService]

	var internal, external int
	for name, attach := range fwd.Networks {
		if spec.Networks[name].Internal {
			internal++
			if !slices.Contains(attach.Aliases, GatewayHost) {
				t.Errorf("forwarder lacks alias %q on internal network", GatewayHost)
			}
		} else {
			external++
		}
	}
	if internal != 1 || external != 1 {
		t.Errorf("forwarder on %d internal and %d external networks, want 1 and 1", internal, external)
	}

	want := []string{"TCP-LISTEN:8080,fork,reuseaddr", "TCP:host.docker.internal:8080"}
	if !slices.Equal(fwd.Command, want) {
		t.Errorf("forwarder command = %v, want %v", fwd.Command, want)
	}
}

func TestGatewayIsConfigurable(t *testing.T) {
	in := testInputs()
	in.Gateway = Gateway{Host: "10.0.0.5", Port: 9292}
	fwd := Build(in).Services[ForwarderService]

	if got := fwd.Command[len(fwd.Command)-1]; got != "TCP:10.0.0.5:9292" {
		t.Errorf("forwarder target = %q", got)
	}
}

func TestServicesAreHardened(t *testing.T) {
	spec := Build(testInputs())
	for name, svc := range spec.Services {
		if !slices.Equal(svc.CapDrop, []string{"ALL"}) {
			t.Errorf("%s cap_drop = %v", name, svc.CapDrop)
		}
		if !slices.Contains(svc.SecurityOpt, "no-new-privileges:true") {
			t.Errorf("%s lacks no-new-privileges", name)
		}
	}
	if !spec.Services[ForwarderService].ReadOnly {
		t.Error("forwarder root filesystem is writable")
	}
}

func TestAgentRunsAsHostUserInWorkspace(t *testing.T) {
	agent := Build(testInputs()).Services[AgentService]

	if agent.User != "501:20" {
		t.Errorf("user = %q", agent.User)
	}
	if agent.WorkingDir != Workspace {
		t.Errorf("working_dir = %q", agent.WorkingDir)
	}
	if agent.Environment["HOME"] != Home || agent.Environment["FOO"] != "bar" {
		t.Errorf("environment = %v", agent.Environment)
	}
	if len(agent.Tmpfs) != 1 || !strings.HasPrefix(agent.Tmpfs[0], Home+":uid=501,gid=20") {
		t.Errorf("tmpfs = %v", agent.Tmpfs)
	}
}

func TestMounts(t *testing.T) {
	agent := Build(testInputs()).Services[AgentService]

	want := []Volume{
		{Type: "bind", Source: "/Users/me/Projects/foo", Target: Workspace},
		{Type: "bind", Source: "/Users/me/Projects/foo/.git", Target: "/workspace/.git"},
		{Type: "bind", Source: "/tmp/cfg.json", Target: "/etc/localllm/opencode.json", ReadOnly: true},
	}
	if !slices.Equal(agent.Volumes, want) {
		t.Errorf("volumes = %+v, want %+v", agent.Volumes, want)
	}
}

func TestCommandOverride(t *testing.T) {
	if cmd := Build(testInputs()).Services[AgentService].Command; cmd != nil {
		t.Errorf("default command = %v, want image default", cmd)
	}
	in := testInputs()
	in.Command = []string{"opencode", "run", "hi"}
	if cmd := Build(in).Services[AgentService].Command; !slices.Equal(cmd, in.Command) {
		t.Errorf("command = %v", cmd)
	}
}

func TestProjectName(t *testing.T) {
	a := ProjectName("/Users/me/Projects/My App")
	if a != ProjectName("/Users/me/Projects/My App") {
		t.Error("project name not stable")
	}
	if a == ProjectName("/Users/me/Other/My App") {
		t.Error("different paths share a project name")
	}
	if !strings.HasPrefix(a, "localllm-my-app-") {
		t.Errorf("project name = %q", a)
	}
}

func TestParseGateway(t *testing.T) {
	tests := []struct {
		raw     string
		want    Gateway
		wantErr bool
	}{
		{raw: "http://host.docker.internal:8080", want: Gateway{"host.docker.internal", 8080}},
		{raw: "http://host.docker.internal:8080/", want: Gateway{"host.docker.internal", 8080}},
		{raw: "http://gw", want: Gateway{"gw", 80}},
		{raw: "https://gw:443", wantErr: true},
		{raw: "http://gw:8080/v1", wantErr: true},
		{raw: "http://gw:0", wantErr: true},
		{raw: "gw:8080", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseGateway(tt.raw)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseGateway(%q) err = %v, wantErr %v", tt.raw, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("ParseGateway(%q) = %+v, want %+v", tt.raw, got, tt.want)
		}
	}
}

func TestStateVolume(t *testing.T) {
	a := StateVolume("/Users/me/Projects/a", "opencode")
	if a != StateVolume("/Users/me/Projects/a", "opencode") {
		t.Error("state volume name not stable")
	}
	for _, other := range []string{
		StateVolume("/Users/me/Projects/b", "opencode"),
		StateVolume("/Users/me/Other/a", "opencode"),
		StateVolume("/Users/me/Projects/a", "pi"),
	} {
		if other == a {
			t.Errorf("%q is shared", a)
		}
	}
	if !regexp.MustCompile(`^localllm-state-opencode-[0-9a-f]{32}$`).MatchString(a) {
		t.Errorf("state volume = %q", a)
	}
}

func TestStateVolumeMount(t *testing.T) {
	in := testInputs()
	in.StateVolume = StateVolume(in.ProjectDir, "opencode")
	spec := Build(in)
	agent := spec.Services[AgentService]

	var found bool
	for _, v := range agent.Volumes {
		if v.Target == StateDir {
			found = true
			if v.Type != "volume" || v.ReadOnly || spec.Volumes[v.Source].Name != in.StateVolume {
				t.Errorf("state mount = %+v, volumes = %+v", v, spec.Volumes)
			}
		}
	}
	if !found {
		t.Errorf("no mount at %s in %+v", StateDir, agent.Volumes)
	}
	for k, want := range map[string]string{
		"XDG_DATA_HOME":  StateDir + "/data",
		"XDG_STATE_HOME": StateDir + "/state",
		"XDG_CACHE_HOME": StateDir + "/cache",
	} {
		if agent.Environment[k] != want {
			t.Errorf("%s = %q, want %q", k, agent.Environment[k], want)
		}
	}

	if spec := Build(testInputs()); spec.Volumes != nil || spec.Services[AgentService].Environment["XDG_DATA_HOME"] != "" {
		t.Errorf("state volume without StateVolume: %+v", spec.Volumes)
	}
}

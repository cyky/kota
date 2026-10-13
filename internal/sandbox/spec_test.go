package sandbox

import (
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
		if !spec.Networks[name].Internal {
			t.Errorf("agent network %q is not internal", name)
		}
	}
}

func TestForwarderBridgesToGatewayOnly(t *testing.T) {
	spec := Build(testInputs())
	fwd := spec.Services[forwarderSvc]

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
	fwd := Build(in).Services[forwarderSvc]

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
	if !spec.Services[forwarderSvc].ReadOnly {
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

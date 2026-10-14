package sandbox

import (
	"encoding/json"
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

func TestScratchDirsAreAgentOwnedTmpfs(t *testing.T) {
	in := testInputs()
	in.Scratch = []string{Home + "/.pi/agent"}
	agent := Build(in).Services[AgentService]

	want := []string{
		Home + ":uid=501,gid=20,mode=0700",
		Home + "/.pi/agent:uid=501,gid=20,mode=0700",
	}
	if !slices.Equal(agent.Tmpfs, want) {
		t.Errorf("tmpfs = %v, want %v", agent.Tmpfs, want)
	}
}

func TestMounts(t *testing.T) {
	agent := Build(testInputs()).Services[AgentService]

	want := []Volume{
		{Type: "bind", Source: "/Users/me/Projects/foo", Target: Workspace},
		{Type: "bind", Source: "/Users/me/Projects/foo/.git", Target: "/workspace/.git"},
		{Type: "bind", Source: "/tmp/cfg.json", Target: "/etc/localllm/opencode.json", ReadOnly: true},
	}
	if binds := slices.DeleteFunc(slices.Clone(agent.Volumes), func(v Volume) bool { return v.Type != "bind" }); !slices.Equal(binds, want) {
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

	if spec := Build(testInputs()); spec.Volumes[stateKey].Name != "" || spec.Services[AgentService].Environment["XDG_DATA_HOME"] != "" {
		t.Errorf("state volume without StateVolume: %+v", spec.Volumes)
	}
}

func TestLiteralEscapesDollars(t *testing.T) {
	in := testInputs()
	in.ProjectDir = "/Users/me/Projects/a$b"
	in.Env = map[string]string{"GIT_CONFIG_VALUE_1": "Pay$al"}
	in.Command = []string{"sh", "-c", "echo $HOME ${USER}"}
	in.StateVolume = StateVolume(in.ProjectDir, "opencode")
	spec := Build(in)
	lit := spec.Literal()
	agent := lit.Services[AgentService]

	if want := []string{"sh", "-c", "echo $$HOME $${USER}"}; !slices.Equal(agent.Command, want) {
		t.Errorf("command = %q, want %q", agent.Command, want)
	}
	if agent.Environment["GIT_CONFIG_VALUE_1"] != "Pay$$al" {
		t.Errorf("env = %q", agent.Environment["GIT_CONFIG_VALUE_1"])
	}
	if agent.Volumes[0].Source != "/Users/me/Projects/a$$b" {
		t.Errorf("project source = %q", agent.Volumes[0].Source)
	}
	// The original is untouched.
	if spec.Services[AgentService].Command[2] != "echo $HOME ${USER}" || spec.Services[AgentService].Volumes[0].Source != in.ProjectDir {
		t.Error("Literal modified the spec")
	}
}

// marshalled is the agent service as written to the compose file.
func marshalled(t *testing.T, c Compose) map[string]any {
	t.Helper()
	data, err := json.Marshal(c.Literal())
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Services map[string]map[string]any `json:"services"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out.Services[AgentService]
}

func TestGatewayKeyOnlyInRunMode(t *testing.T) {
	cases := []struct {
		name    string
		mode    Mode
		key     bool
		wantKey bool
	}{
		{"run with key", ModeRun, true, true},
		{"run without key", ModeRun, false, false},
		{"prep with key", ModePrep, true, false},
		{"prep without key", ModePrep, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := testInputs()
			in.Mode, in.GatewayKey = c.mode, c.key
			spec := Build(in)
			agent := spec.Services[AgentService]
			if got := slices.Contains(agent.PassEnv, GatewayKeyEnv); got != c.wantKey {
				t.Errorf("key passed = %v, want %v", got, c.wantKey)
			}
			if _, ok := agent.Environment[GatewayKeyEnv]; ok {
				t.Error("key value in the spec's environment")
			}
			if _, ok := spec.Services[ForwarderService].Environment[GatewayKeyEnv]; ok {
				t.Error("forwarder gets the key")
			}

			// In the compose file the key is a name without a value; the
			// other variables keep theirs.
			env, _ := marshalled(t, spec)["environment"].(map[string]any)
			v, ok := env[GatewayKeyEnv]
			if ok != c.wantKey || v != nil {
				t.Errorf("compose environment %s = %v (present %v), want present %v with null value", GatewayKeyEnv, v, ok, c.wantKey)
			}
			if env["FOO"] != "bar" || env["HOME"] != Home {
				t.Errorf("compose environment = %v", env)
			}
		})
	}
}

// volumeAt returns the agent's volume mounted at target and the named
// volume behind it.
func volumeAt(spec Compose, service, target string) (Volume, NamedVolume, bool) {
	for _, v := range spec.Services[service].Volumes {
		if v.Target == target {
			return v, spec.Volumes[v.Source], true
		}
	}
	return Volume{}, NamedVolume{}, false
}

func depInputs(mode Mode) Inputs {
	in := testInputs()
	in.Mode = mode
	in.GatewayKey = true
	in.Deps = []Mount{
		{Source: DepVolume(in.ProjectDir, "node_modules"), Target: Workspace + "/node_modules"},
		{Source: DepVolume(in.ProjectDir, "packages/a/node_modules"), Target: Workspace + "/packages/a/node_modules"},
	}
	return in
}

func TestPrepHasNetworkButNoGateway(t *testing.T) {
	spec := Build(depInputs(ModePrep))
	agent := spec.Services[AgentService]

	if _, ok := spec.Services[ForwarderService]; ok {
		t.Error("prep has a forwarder")
	}
	if len(agent.Networks) != 1 {
		t.Fatalf("agent networks = %v", agent.Networks)
	}
	for name := range agent.Networks {
		if spec.Networks[name].Internal {
			t.Errorf("prep agent is on internal network %q", name)
		}
	}
	for name, n := range spec.Networks {
		if n.Internal {
			t.Errorf("prep spec has internal network %q", name)
		}
	}
	if len(agent.DependsOn) > 0 || len(agent.PassEnv) > 0 {
		t.Errorf("prep depends_on = %v, pass env = %v", agent.DependsOn, agent.PassEnv)
	}
	if v, ok := agent.Environment["GOPROXY"]; ok {
		t.Errorf("prep GOPROXY = %q", v)
	}
}

func TestRunIsOfflineForPackageManagers(t *testing.T) {
	env := Build(depInputs(ModeRun)).Services[AgentService].Environment
	if env["GOPROXY"] != "off" || env["COREPACK_ENABLE_NETWORK"] != "0" {
		t.Errorf("run environment = %v", env)
	}
}

func TestDependencyVolumes(t *testing.T) {
	for _, mode := range []Mode{ModeRun, ModePrep} {
		in := depInputs(mode)
		spec := Build(in)
		for _, d := range in.Deps {
			v, named, ok := volumeAt(spec, AgentService, d.Target)
			if !ok || v.Type != "volume" || v.ReadOnly || named.Name != d.Source || named.External {
				t.Errorf("mode %d: mount at %s = %+v (%+v, found %v), want writable per-project volume %s", mode, d.Target, v, named, ok, d.Source)
			}
		}
	}
}

func TestSharedCaches(t *testing.T) {
	cases := []struct {
		mode   Mode
		target string
		env    string
		// mounted and readOnly are what the mode should get.
		mounted, readOnly bool
	}{
		{ModePrep, GoModCache, "GOMODCACHE", true, false},
		{ModePrep, CorepackHome, "COREPACK_HOME", true, false},
		{ModePrep, PnpmStore, "", true, false},
		{ModeRun, GoModCache, "GOMODCACHE", true, true},
		{ModeRun, CorepackHome, "COREPACK_HOME", true, true},
		{ModeRun, PnpmStore, "", false, false},
	}
	for _, c := range cases {
		spec := Build(depInputs(c.mode))
		v, named, ok := volumeAt(spec, AgentService, c.target)
		if ok != c.mounted {
			t.Errorf("mode %d: %s mounted = %v, want %v", c.mode, c.target, ok, c.mounted)
			continue
		}
		if !ok {
			continue
		}
		if v.ReadOnly != c.readOnly || !named.External || !slices.Contains(SharedCacheVolumes(), named.Name) {
			t.Errorf("mode %d: %s = %+v (%+v), want read-only %v, shared", c.mode, c.target, v, named, c.readOnly)
		}
		if c.env != "" && spec.Services[AgentService].Environment[c.env] != c.target {
			t.Errorf("mode %d: %s = %q", c.mode, c.env, spec.Services[AgentService].Environment[c.env])
		}
	}
}

func TestVolumesServiceOnlyChowns(t *testing.T) {
	if _, ok := Build(testInputs()).Services[VolumesService]; ok {
		t.Error("volumes service without dependency volumes")
	}
	for _, mode := range []Mode{ModeRun, ModePrep} {
		in := depInputs(mode)
		spec := Build(in)
		s, ok := spec.Services[VolumesService]
		if !ok {
			t.Fatalf("mode %d: no %s service", mode, VolumesService)
		}
		if !slices.Equal(s.CapAdd, []string{"CHOWN"}) || !slices.Equal(s.CapDrop, []string{"ALL"}) || s.NetworkMode != "none" || len(s.Networks) > 0 || !s.ReadOnly {
			t.Errorf("mode %d: volumes service = %+v", mode, s)
		}
		if len(s.Volumes) != len(in.Deps) {
			t.Errorf("mode %d: volumes service mounts %+v, want only the dependency volumes", mode, s.Volumes)
		}
		for i, v := range s.Volumes {
			if v.Type != "volume" || spec.Volumes[v.Source].Name != in.Deps[i].Source || strings.HasPrefix(v.Target, Workspace) {
				t.Errorf("mode %d: volumes service mount %+v", mode, v)
			}
		}
		if s.Command[4] != "501:20" || !slices.Contains(s.Command, in.Deps[1].Target) {
			t.Errorf("mode %d: command = %q", mode, s.Command)
		}
		if slices.Contains(spec.Services[AgentService].DependsOn, VolumesService) {
			t.Errorf("mode %d: agent waits for the volumes service; the driver runs it once", mode)
		}
	}
}

func TestDepVolume(t *testing.T) {
	a := DepVolume("/Users/me/Projects/a", "node_modules")
	if a != DepVolume("/Users/me/Projects/a", "node_modules") {
		t.Error("dependency volume name not stable")
	}
	for _, other := range []string{
		DepVolume("/Users/me/Projects/b", "node_modules"),
		DepVolume("/Users/me/Projects/a", "packages/x/node_modules"),
	} {
		if other == a {
			t.Errorf("%q is shared", a)
		}
	}
	if !strings.HasPrefix(a, DepVolumePrefix("/Users/me/Projects/a")) || strings.HasPrefix(a, DepVolumePrefix("/Users/me/Projects/b")) {
		t.Errorf("prefix of %q", a)
	}
	if !regexp.MustCompile(`^localllm-deps-[0-9a-f]{32}-[0-9a-f]{12}$`).MatchString(a) {
		t.Errorf("dependency volume = %q", a)
	}
}

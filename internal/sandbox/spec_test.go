package sandbox

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
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
		ReadOnly:       []Mount{{Source: "/tmp/cfg.json", Target: "/etc/kota/opencode.json"}},
		Env:            map[string]string{"FOO": "bar"},
	}
}

// compose is Build's spec alone.
func compose(in Inputs) Compose {
	spec, _ := Build(in)
	return spec
}

// TestExpectMatchesAgentVolumes checks, across modes and inputs, that the
// probes expect exactly the agent's read-only, pinned and masked volumes and
// its state volume: a mount added to the spec can't be left unprobed.
func TestExpectMatchesAgentVolumes(t *testing.T) {
	full := func(mode Mode) Inputs {
		in := testInputs()
		in.Mode = mode
		in.ReadOnly = append(in.ReadOnly,
			Mount{Source: "/tmp/empty-file", Target: "/workspace/.env", Mask: true},
			Mount{Source: "/Users/me/Projects/foo/.envrc", Target: "/workspace/.envrc"},
			Mount{Source: "/Users/me/Projects/ref", Target: ReadOnlyRoot + "/ref"})
		in.Pinned = append(in.Pinned, Mount{Source: "/tmp/empty-dir", Target: "/workspace/.ssh", Mask: true})
		in.StateVolume = "kota-state-opencode-x"
		in.Scratch = []string{Home + "/.pi/agent"}
		in.Deps = []Mount{{Source: "kota-deps-x-1", Target: "/workspace/node_modules"}}
		in.Engine = &EngineRoute{Host: "host.docker.internal", APIPort: 2376, Forwards: []PortForward{{Listen: 2376, Target: 50000}}, CertDir: "/tmp/certs"}
		return in
	}
	cases := []struct {
		name string
		in   Inputs
		want Expect
	}{
		{"plain run", testInputs(), Expect{
			ReadOnly: []string{"/etc/kota/opencode.json", GoModCache, CorepackHome},
			Pinned:   []string{"/workspace/.git"},
		}},
		{"full run", full(ModeRun), Expect{
			ReadOnly: []string{"/etc/kota/opencode.json", "/workspace/.env", "/workspace/.envrc", ReadOnlyRoot + "/ref", GoModCache, CorepackHome, EngineCertDir},
			Pinned:   []string{"/workspace/.git", "/workspace/.ssh"},
			Masked:   []string{"/workspace/.ssh", "/workspace/.env"},
			State:    StateDir,
		}},
		{"full prep", full(ModePrep), Expect{
			ReadOnly: []string{"/etc/kota/opencode.json", "/workspace/.env", "/workspace/.envrc", ReadOnlyRoot + "/ref", GoModCache, CorepackHome, PnpmStore},
			Pinned:   []string{"/workspace/.git", "/workspace/.ssh"},
			Masked:   []string{"/workspace/.ssh", "/workspace/.env"},
			State:    StateDir,
		}},
		{"full fetch", full(ModeFetch), Expect{
			ReadOnly: []string{"/etc/kota/opencode.json", "/workspace/.env", "/workspace/.envrc", ReadOnlyRoot + "/ref"},
			Pinned:   []string{"/workspace/.git", "/workspace/.ssh"},
			Masked:   []string{"/workspace/.ssh", "/workspace/.env"},
			State:    StateDir,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec, got := Build(c.in)
			if !slices.Equal(got.ReadOnly, c.want.ReadOnly) || !slices.Equal(got.Pinned, c.want.Pinned) ||
				!slices.Equal(got.Masked, c.want.Masked) || got.State != c.want.State {
				t.Errorf("expect = %+v\nwant     %+v", got, c.want)
			}
			// Every volume of the agent is accounted for: read-only, pinned,
			// the state, one of the writable workspace and dependencies, or
			// in fetch, the caches it fills.
			for _, v := range spec.Services[AgentService].Volumes {
				switch {
				case v.ReadOnly:
					if !slices.Contains(got.ReadOnly, v.Target) {
						t.Errorf("read-only %s not probed", v.Target)
					}
				case v.Type == "bind" && v.Target != Workspace:
					if !slices.Contains(got.Pinned, v.Target) {
						t.Errorf("pinned %s not probed", v.Target)
					}
				case v.Target == StateDir, v.Target == Workspace, v.Target == "/workspace/node_modules":
				case c.in.Mode == ModeFetch && slices.Contains([]string{GoModCache, CorepackHome, PnpmStore}, v.Target):
				default:
					t.Errorf("volume %+v not accounted for", v)
				}
			}
			// The mounts allowed are exactly the agent's volumes and tmpfs
			// directories.
			agent := spec.Services[AgentService]
			var mounts []string
			for _, v := range agent.Volumes {
				mounts = append(mounts, v.Target)
			}
			for _, tmpfs := range agent.Tmpfs {
				dir, _, _ := strings.Cut(tmpfs, ":")
				mounts = append(mounts, dir)
			}
			slices.Sort(mounts)
			allowed := slices.Sorted(slices.Values(got.Mounts))
			if !slices.Equal(allowed, mounts) {
				t.Errorf("mounts = %v, want %v", allowed, mounts)
			}
		})
	}
}

func TestAgentIsOnlyOnInternalNetwork(t *testing.T) {
	spec := compose(testInputs())
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
	spec := compose(testInputs())
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

	want := []string{"TCP-LISTEN:8080,fork,reuseaddr,backlog=1024", "TCP:host.docker.internal:8080"}
	if !slices.Equal(fwd.Command, want) {
		t.Errorf("forwarder command = %v, want %v", fwd.Command, want)
	}
}

func TestForwarderJoinsGatewayNetwork(t *testing.T) {
	in := testInputs()
	in.Gateway = Gateway{Host: "model-server", Port: 4000, Network: "models_internal"}
	spec := compose(in)
	fwd := spec.Services[ForwarderService]

	var joined []string
	for name, attach := range fwd.Networks {
		n := spec.Networks[name]
		switch {
		case n.Internal:
			if !slices.Contains(attach.Aliases, GatewayHost) {
				t.Errorf("forwarder lacks alias %q on internal network", GatewayHost)
			}
		case n.External:
			joined = append(joined, n.Name)
		default:
			t.Errorf("forwarder is on session network %q, want only internal and the gateway's", name)
		}
	}
	if !slices.Equal(joined, []string{"models_internal"}) {
		t.Errorf("forwarder joins external networks %v, want [models_internal]", joined)
	}
	if got := fwd.Command[len(fwd.Command)-1]; got != "TCP:model-server:4000" {
		t.Errorf("forwarder target = %q", got)
	}
	if _, ok := spec.Networks[egressNetwork]; ok {
		t.Errorf("egress declared without a service on it")
	}
	if len(spec.Services[AgentService].Networks) != 1 {
		t.Errorf("agent networks = %v, want only the internal one", spec.Services[AgentService].Networks)
	}
}

func TestEngineForwarderKeepsEgressWithGatewayNetwork(t *testing.T) {
	in := testInputs()
	in.Gateway = Gateway{Host: "model-server", Port: 4000, Network: "models_internal"}
	in.Engine = &EngineRoute{Host: "host.docker.internal", APIPort: 2376, Forwards: []PortForward{{Listen: 2376, Target: 50000}}, CertDir: "/tmp/certs"}
	spec := compose(in)

	if _, ok := spec.Services[EngineService].Networks[egressNetwork]; !ok {
		t.Errorf("engine forwarder networks = %v, want egress", spec.Services[EngineService].Networks)
	}
	if _, ok := spec.Networks[egressNetwork]; !ok {
		t.Errorf("egress not declared")
	}
	if _, ok := spec.Services[ForwarderService].Networks[egressNetwork]; ok {
		t.Errorf("gateway forwarder is on egress")
	}
}

func TestNetworkName(t *testing.T) {
	in := testInputs()
	in.Gateway = Gateway{Host: "model-server", Port: 4000, Network: "models_internal"}
	spec := compose(in)
	if got := spec.NetworkName(gatewayNetwork); got != "models_internal" {
		t.Errorf("gateway network name = %q, want models_internal", got)
	}
	if got, want := spec.NetworkName(internalNetwork), spec.Name+"_"+internalNetwork; got != want {
		t.Errorf("internal network name = %q, want %q", got, want)
	}
}

func TestGatewayAddrTellsNetworksApart(t *testing.T) {
	a := Gateway{Host: "model-server", Port: 4000, Network: "a"}
	b := Gateway{Host: "model-server", Port: 4000, Network: "b"}
	host := Gateway{Host: "model-server", Port: 4000}
	if a.Addr() == b.Addr() || a.Addr() == host.Addr() {
		t.Errorf("addrs %q, %q, %q not distinct", a.Addr(), b.Addr(), host.Addr())
	}
	if got := host.Addr(); got != "model-server:4000" {
		t.Errorf("host gateway addr = %q", got)
	}
}

func TestGatewayIsConfigurable(t *testing.T) {
	in := testInputs()
	in.Gateway = Gateway{Host: "10.0.0.5", Port: 9292}
	fwd := compose(in).Services[ForwarderService]

	if got := fwd.Command[len(fwd.Command)-1]; got != "TCP:10.0.0.5:9292" {
		t.Errorf("forwarder target = %q", got)
	}
}

func TestServicesAreHardened(t *testing.T) {
	spec := compose(testInputs())
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
	agent := compose(testInputs()).Services[AgentService]

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
	agent := compose(in).Services[AgentService]

	want := []string{
		Home + ":uid=501,gid=20,mode=0700",
		Home + "/.pi/agent:uid=501,gid=20,mode=0700",
	}
	if !slices.Equal(agent.Tmpfs, want) {
		t.Errorf("tmpfs = %v, want %v", agent.Tmpfs, want)
	}
}

func TestMounts(t *testing.T) {
	agent := compose(testInputs()).Services[AgentService]

	want := []Volume{
		{Type: "bind", Source: "/Users/me/Projects/foo", Target: Workspace},
		{Type: "bind", Source: "/Users/me/Projects/foo/.git", Target: "/workspace/.git"},
		{Type: "bind", Source: "/tmp/cfg.json", Target: "/etc/kota/opencode.json", ReadOnly: true},
	}
	if binds := slices.DeleteFunc(slices.Clone(agent.Volumes), func(v Volume) bool { return v.Type != "bind" }); !slices.Equal(binds, want) {
		t.Errorf("volumes = %+v, want %+v", agent.Volumes, want)
	}
}

func TestCommandOverride(t *testing.T) {
	if cmd := compose(testInputs()).Services[AgentService].Command; cmd != nil {
		t.Errorf("default command = %v, want image default", cmd)
	}
	in := testInputs()
	in.Command = []string{"opencode", "run", "hi"}
	if cmd := compose(in).Services[AgentService].Command; !slices.Equal(cmd, in.Command) {
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
	if !strings.HasPrefix(a, "kota-my-app-") {
		t.Errorf("project name = %q", a)
	}
}

func TestKeyNamesComposeProject(t *testing.T) {
	in := testInputs()
	in.ProjectDir = "/cache/kota/copies/foo-copy"
	in.Key = "/Users/me/Projects/foo"
	spec := compose(in)
	if spec.Name != ProjectName(in.Key) {
		t.Errorf("compose project = %q, want the key's %q", spec.Name, ProjectName(in.Key))
	}
	if v := spec.Services[AgentService].Volumes[0]; v.Source != in.ProjectDir || v.Target != Workspace {
		t.Errorf("workspace mount = %+v, want %s", v, in.ProjectDir)
	}
}

func TestParseGateway(t *testing.T) {
	tests := []struct {
		raw     string
		want    Gateway
		wantErr bool
	}{
		{raw: "http://host.docker.internal:8080", want: Gateway{Host: "host.docker.internal", Port: 8080}},
		{raw: "http://host.docker.internal:8080/", want: Gateway{Host: "host.docker.internal", Port: 8080}},
		{raw: "http://gw", want: Gateway{Host: "gw", Port: 80}},
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
	if !regexp.MustCompile(`^kota-state-opencode-[0-9a-f]{32}$`).MatchString(a) {
		t.Errorf("state volume = %q", a)
	}
}

func TestStateVolumeMount(t *testing.T) {
	in := testInputs()
	in.StateVolume = StateVolume(in.ProjectDir, "opencode")
	spec := compose(in)
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

	if spec := compose(testInputs()); spec.Volumes[stateKey].Name != "" || spec.Services[AgentService].Environment["XDG_DATA_HOME"] != "" {
		t.Errorf("state volume without StateVolume: %+v", spec.Volumes)
	}
}

func TestLiteralEscapesDollars(t *testing.T) {
	in := testInputs()
	in.ProjectDir = "/Users/me/Projects/a$b"
	in.Env = map[string]string{"GIT_CONFIG_VALUE_1": "Pay$al"}
	in.Command = []string{"sh", "-c", "echo $HOME ${USER}"}
	in.StateVolume = StateVolume(in.ProjectDir, "opencode")
	spec := compose(in)
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
			spec := compose(in)
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
	for _, mode := range []Mode{ModePrep, ModeFetch} {
		t.Run(strconv.Itoa(int(mode)), func(t *testing.T) { testNetworkNoGateway(t, mode) })
	}
}

func testNetworkNoGateway(t *testing.T, mode Mode) {
	spec := compose(depInputs(mode))
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
	env := compose(depInputs(ModeRun)).Services[AgentService].Environment
	if env["GOPROXY"] != "off" || env["COREPACK_ENABLE_NETWORK"] != "0" {
		t.Errorf("run environment = %v", env)
	}
}

func TestDependencyVolumes(t *testing.T) {
	for _, mode := range []Mode{ModeRun, ModePrep} {
		in := depInputs(mode)
		spec := compose(in)
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
		{ModeFetch, GoModCache, "GOMODCACHE", true, false},
		{ModeFetch, CorepackHome, "COREPACK_HOME", true, false},
		{ModeFetch, PnpmStore, "", true, false},
		{ModePrep, GoModCache, "GOMODCACHE", true, true},
		{ModePrep, CorepackHome, "COREPACK_HOME", true, true},
		{ModePrep, PnpmStore, "", true, true},
		{ModeRun, GoModCache, "GOMODCACHE", true, true},
		{ModeRun, CorepackHome, "COREPACK_HOME", true, true},
		{ModeRun, PnpmStore, "", false, false},
	}
	for _, c := range cases {
		spec := compose(depInputs(c.mode))
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
	if _, ok := compose(testInputs()).Services[VolumesService]; ok {
		t.Error("volumes service without dependency volumes")
	}
	for _, mode := range []Mode{ModeRun, ModePrep} {
		in := depInputs(mode)
		spec := compose(in)
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

// TestProjectImageOnlyForAgent checks a project image runs the agent in
// every mode, while the root volumes service keeps the base image.
func TestProjectImageOnlyForAgent(t *testing.T) {
	for _, mode := range []Mode{ModeRun, ModePrep, ModeFetch} {
		in := depInputs(mode)
		in.AgentImage, in.HelperImage = "kota-project-abc:latest", "kota-base:latest"
		spec := compose(in)
		if got := spec.Services[AgentService].Image; got != in.AgentImage {
			t.Errorf("mode %d: agent image = %s", mode, got)
		}
		if got := spec.Services[VolumesService].Image; got != in.HelperImage {
			t.Errorf("mode %d: volumes service image = %s, want the base image", mode, got)
		}
		if fwd, ok := spec.Services[ForwarderService]; ok && fwd.Image != in.ForwarderImage {
			t.Errorf("mode %d: forwarder image = %s", mode, fwd.Image)
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
	if !regexp.MustCompile(`^kota-deps-[0-9a-f]{32}-[0-9a-f]{12}$`).MatchString(a) {
		t.Errorf("dependency volume = %q", a)
	}
}

// The witness has the agent's project binds and nothing else: no network,
// environment, config, caches or dependency volumes, in every mode.
func TestWitness(t *testing.T) {
	for _, mode := range []Mode{ModeRun, ModePrep, ModeFetch} {
		in := testInputs()
		in.Mode = mode
		in.HelperImage = "helper:test"
		in.ReadOnly = append(in.ReadOnly, Mount{Source: "/tmp/empty-file", Target: "/workspace/.env", Mask: true})
		in.StateVolume = "kota-state-opencode-x"
		in.Deps = []Mount{{Source: "kota-deps-x-1", Target: "/workspace/node_modules"}}
		spec := compose(in)
		w, ok := spec.Services[WitnessService]
		if !ok {
			t.Fatalf("mode %d: no witness", mode)
		}
		want := []Volume{
			{Type: "bind", Source: "/Users/me/Projects/foo", Target: Workspace},
			{Type: "bind", Source: "/Users/me/Projects/foo/.git", Target: "/workspace/.git"},
			{Type: "bind", Source: "/tmp/empty-file", Target: "/workspace/.env", ReadOnly: true},
		}
		if !slices.Equal(w.Volumes, want) {
			t.Errorf("mode %d: witness volumes = %+v", mode, w.Volumes)
		}
		if w.Image != "helper:test" || w.User != "501:20" || w.NetworkMode != "none" || len(w.Networks) > 0 ||
			!w.ReadOnly || len(w.Environment) > 0 || !slices.Equal(w.CapDrop, []string{"ALL"}) {
			t.Errorf("mode %d: witness = %+v", mode, w)
		}
	}
}

func TestProjectVolumes(t *testing.T) {
	const project = "/projects/app"
	want := []string{
		StateVolume(project, "opencode"), StateVolume(project, "pi"), StateVolume(project, "retired-agent"),
		StateVolume(project, "pi"+NoHistoryState),
		DepVolume(project, "node_modules"), DepVolume(project, "old-package/node_modules"),
	}
	slices.Sort(want)
	available := append(slices.Clone(want),
		StateVolume("/projects/app2", "opencode"), DepVolume("/projects/app2", "node_modules"),
		DepVolumePrefix(project)+"abc", DepVolume(project, "node_modules")+"-backup",
		StateVolume(project, "opencode")+"-backup", "unrelated-volume", want[0],
	)
	available = append(available, SharedCacheVolumes()...)
	if got := ProjectVolumes(project, available); !slices.Equal(got, want) {
		t.Errorf("project volumes = %v, want %v", got, want)
	}
	if got := ProjectVolumes(project, nil); len(got) != 0 {
		t.Errorf("missing project has volumes: %v", got)
	}
}

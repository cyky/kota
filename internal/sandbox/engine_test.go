package sandbox

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func engineInputs() Inputs {
	in := testInputs()
	in.Engine = &EngineRoute{
		Host:     "host.docker.internal",
		APIPort:  2376,
		Forwards: []PortForward{{Listen: 2376, Target: 51000}, {Listen: 40000, Target: 51001}, {Listen: 40001, Target: 51002}},
		CertDir:  "/cache/sessions/x/engine-client",
	}
	return in
}

var engineEnv = map[string]string{
	"DOCKER_HOST":                  "tcp://engine:2376",
	"DOCKER_TLS_VERIFY":            "1",
	"DOCKER_CERT_PATH":             EngineCertDir,
	"TESTCONTAINERS_HOST_OVERRIDE": "engine",
	"TESTCONTAINERS_RYUK_DISABLED": "true",
}

func TestEngineEnvOnlyForEngineProjects(t *testing.T) {
	agent := compose(engineInputs()).Services[AgentService]
	for k, v := range engineEnv {
		if agent.Environment[k] != v {
			t.Errorf("%s = %q, want %q", k, agent.Environment[k], v)
		}
	}
	plain := compose(testInputs())
	for k := range engineEnv {
		if _, ok := plain.Services[AgentService].Environment[k]; ok {
			t.Errorf("%s set without an engine", k)
		}
	}
	if _, ok := plain.Services[EngineService]; ok {
		t.Error("engine forwarder without an engine")
	}
}

func TestEngineAvailabilitySignal(t *testing.T) {
	off := testInputs()
	off.EngineOff = true
	// EngineOff alongside a route is a caller bug; the route wins.
	both := engineInputs()
	both.EngineOff = true
	cases := []struct {
		name string
		in   Inputs
		want string // "" means unset
	}{
		{name: "engine", in: engineInputs(), want: EngineAvailable},
		{name: "no engine section", in: testInputs()},
		{name: "--no-engine", in: off, want: EngineUnavailable},
		{name: "route and off", in: both, want: EngineAvailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := compose(c.in)
			env := spec.Services[AgentService].Environment
			got, ok := env[EngineEnv]
			if got != c.want || ok != (c.want != "") {
				t.Errorf("%s = %q (set %v), want %q", EngineEnv, got, ok, c.want)
			}
			if c.want == EngineUnavailable {
				for k := range engineEnv {
					if _, ok := env[k]; ok {
						t.Errorf("%s set with --no-engine", k)
					}
				}
				if _, ok := spec.Services[EngineService]; ok {
					t.Error("engine forwarder with --no-engine")
				}
				if _, _, ok := volumeAt(spec, AgentService, EngineCertDir); ok {
					t.Error("client certs mounted with --no-engine")
				}
			}
		})
	}
}

func TestEngineEnvWinsOverInputs(t *testing.T) {
	in := engineInputs()
	in.Env = map[string]string{"DOCKER_HOST": "unix:///var/run/docker.sock", "TESTCONTAINERS_RYUK_DISABLED": "false"}
	env := compose(in).Services[AgentService].Environment
	if env["DOCKER_HOST"] != "tcp://engine:2376" || env["TESTCONTAINERS_RYUK_DISABLED"] != "true" {
		t.Errorf("env = %v", env)
	}
}

func TestEngineClientCertsReadOnly(t *testing.T) {
	spec := compose(engineInputs())
	v, _, ok := volumeAt(spec, AgentService, EngineCertDir)
	if !ok || !v.ReadOnly || v.Type != "bind" || v.Source != "/cache/sessions/x/engine-client" {
		t.Errorf("cert mount = %+v (found %v)", v, ok)
	}
}

func TestEngineForwarder(t *testing.T) {
	spec := compose(engineInputs())
	fwd, ok := spec.Services[EngineService]
	if !ok {
		t.Fatal("no engine forwarder")
	}
	if !fwd.ReadOnly || !slices.Equal(fwd.CapDrop, []string{"ALL"}) || !slices.Contains(fwd.SecurityOpt, "no-new-privileges:true") {
		t.Errorf("engine forwarder not hardened: %+v", fwd)
	}
	if fwd.Image != "forwarder:test" || len(fwd.Volumes) != 0 || fwd.User != "" {
		t.Errorf("engine forwarder = %+v", fwd)
	}
	var internal, external int
	for name, attach := range fwd.Networks {
		if spec.Networks[name].Internal {
			internal++
			if !slices.Equal(attach.Aliases, []string{EngineHost}) {
				t.Errorf("engine forwarder aliases = %v", attach.Aliases)
			}
		} else {
			external++
		}
	}
	if internal != 1 || external != 1 {
		t.Errorf("engine forwarder on %d internal and %d external networks", internal, external)
	}
	// After "sh -c script sh host backlog", the arguments are the port pairs.
	args := fwd.Entrypoint
	if len(args) < 6 || args[0] != "sh" || args[4] != "host.docker.internal" || args[5] != "1024" {
		t.Fatalf("entrypoint = %q", args)
	}
	want := []string{"2376", "51000", "40000", "51001", "40001", "51002"}
	if !slices.Equal(args[6:], want) {
		t.Errorf("forwards = %q, want %q", args[6:], want)
	}
	agent := spec.Services[AgentService]
	if !slices.Contains(agent.DependsOn, EngineService) {
		t.Errorf("agent depends on %v", agent.DependsOn)
	}
	if len(agent.Networks) != 1 {
		t.Errorf("agent networks = %v", agent.Networks)
	}
}

// TestEngineKeepsSandboxHardening checks an engine session's agent is as
// locked down as any: unprivileged, no added capabilities, offline, and
// with no Docker socket.
func TestEngineKeepsSandboxHardening(t *testing.T) {
	spec := compose(engineInputs())
	data, err := json.Marshal(spec.Literal())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "docker.sock") || strings.Contains(string(data), "privileged") {
		t.Errorf("spec mentions the Docker socket or privileges: %s", data)
	}
	for name, svc := range spec.Services {
		if name == VolumesService {
			continue
		}
		if len(svc.CapAdd) > 0 || svc.NetworkMode == "host" {
			t.Errorf("%s: cap_add %v, network_mode %q", name, svc.CapAdd, svc.NetworkMode)
		}
	}
	agent := spec.Services[AgentService]
	if agent.User != "501:20" || !slices.Equal(agent.CapDrop, []string{"ALL"}) {
		t.Errorf("agent user %q cap_drop %v", agent.User, agent.CapDrop)
	}
	for name := range agent.Networks {
		if !spec.Networks[name].Internal {
			t.Errorf("agent on non-internal network %s", name)
		}
	}
}

func TestEngineIgnoredInPrep(t *testing.T) {
	for _, mode := range []Mode{ModePrep, ModeFetch} {
		t.Run(strconv.Itoa(int(mode)), func(t *testing.T) {
			in := engineInputs()
			in.Mode = mode
			spec := compose(in)
			in.Engine, in.EngineOff = nil, true
			if _, ok := compose(in).Services[AgentService].Environment[EngineEnv]; ok {
				t.Errorf("%s in prep", EngineEnv)
			}
			if _, ok := spec.Services[EngineService]; ok {
				t.Error("engine forwarder in prep")
			}
			if _, ok := spec.Services[AgentService].Environment["DOCKER_HOST"]; ok {
				t.Error("DOCKER_HOST in prep")
			}
			if _, _, ok := volumeAt(spec, AgentService, EngineCertDir); ok {
				t.Error("client certs mounted in prep")
			}
		})
	}
}

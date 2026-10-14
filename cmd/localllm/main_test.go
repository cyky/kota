package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"localllm/internal/opencode"
	"localllm/internal/pi"
	"localllm/internal/sandbox"
)

// TestSessionMountsNoHostAgentConfig builds a session for each agent against
// a fake HOME that has host agent config and state, and checks none of it is
// mounted.
func TestSessionMountsNoHostAgentConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("LOCALLLM_ALLOWED_ROOT", "")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("LOCALLLM_GATEWAY_KEY", "")
	t.Setenv("LOCALLLM_GATEWAY_KEY_FILE", "")
	hostDirs := []string{".config/opencode", ".local/share/opencode", ".local/state/opencode", ".opencode", ".pi"}
	for _, d := range append(hostDirs, "Projects/app") {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	home, _ = filepath.EvalSymlinks(home)

	cases := []struct {
		agent   string
		configs []string
		env     map[string]string
		command []string
	}{
		{
			agent:   opencode.Agent,
			configs: []string{opencodeConfigPath},
			env:     map[string]string{"OPENCODE_DISABLE_PROJECT_CONFIG": "1"},
			command: []string{"opencode"},
		},
		{
			agent:   pi.Agent,
			configs: []string{piAgentDir + "/settings.json", piAgentDir + "/models.json"},
			env:     map[string]string{"PI_CODING_AGENT_DIR": piAgentDir, "PI_CODING_AGENT_SESSION_DIR": piSessionDir, "PI_OFFLINE": "1"},
			command: []string{"pi"},
		},
	}
	for _, c := range cases {
		t.Run(c.agent, func(t *testing.T) {
			sess, err := prepareSession(sessionArgs{
				agent:      c.agent,
				gatewayURL: defaultGateway,
				dir:        filepath.Join(home, "Projects/app"),
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(sess.cleanup)

			agent := sess.spec.Services[sandbox.AgentService]
			configs := map[string]bool{}
			var state bool
			for _, v := range agent.Volumes {
				for _, d := range hostDirs {
					if v.Source == filepath.Join(home, d) || strings.HasPrefix(v.Source, filepath.Join(home, d)+"/") {
						t.Errorf("host agent directory mounted: %+v", v)
					}
				}
				switch {
				case slices.Contains(c.configs, v.Target):
					configs[v.Target] = true
					if !v.ReadOnly || !strings.HasPrefix(v.Source, sess.sessionDir+"/") {
						t.Errorf("agent config mount = %+v, want read-only from the session dir", v)
					}
				case v.Target == sandbox.StateDir:
					state = true
					if v.Type != "volume" {
						t.Errorf("state mount = %+v, want a named volume", v)
					}
				}
			}
			if len(configs) != len(c.configs) || !state {
				t.Errorf("configs mounted = %v, state mounted = %v in %+v", configs, state, agent.Volumes)
			}
			for _, cfg := range c.configs {
				if !slices.Contains(sess.mounts.ReadOnly, cfg) {
					t.Errorf("probes don't check %s is read-only", cfg)
				}
			}
			want := sandbox.StateVolume(filepath.Join(home, "Projects/app"), c.agent)
			if got := sess.spec.Volumes["state"].Name; got != want {
				t.Errorf("state volume = %q, want %q", got, want)
			}
			for k, v := range c.env {
				if agent.Environment[k] != v {
					t.Errorf("env %s = %q, want %q", k, agent.Environment[k], v)
				}
			}
			if !slices.Equal(agent.Command, c.command) {
				t.Errorf("command = %q, want %q", agent.Command, c.command)
			}
		})
	}
}

// TestGatewayKeyReachesSessionByNameOnly checks a configured key is passed
// to the agent container by name and isn't written to the session dir.
func TestGatewayKeyReachesSessionByNameOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("LOCALLLM_ALLOWED_ROOT", "")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	const secret = "sk-unit-test-secret"
	t.Setenv("LOCALLLM_GATEWAY_KEY", secret)
	t.Setenv("LOCALLLM_GATEWAY_KEY_FILE", "")
	project := filepath.Join(home, "Projects/app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	sess, err := prepareSession(sessionArgs{agent: defaultAgent, gatewayURL: defaultGateway, dir: project})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.cleanup)
	if sess.gatewayKey != secret {
		t.Errorf("session key not loaded")
	}
	agent := sess.spec.Services[sandbox.AgentService]
	if !slices.Contains(agent.PassEnv, sandbox.GatewayKeyEnv) {
		t.Errorf("pass env = %v, want %s", agent.PassEnv, sandbox.GatewayKeyEnv)
	}
	data, err := json.Marshal(sess.spec.Literal())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Error("key in the compose spec")
	}
	var out strings.Builder
	printMounts(&out, sess.spec)
	if strings.Contains(out.String(), secret) || !strings.Contains(out.String(), "gateway key: configured") {
		t.Errorf("check output = %q", out.String())
	}
}

func TestUnknownAgentRefused(t *testing.T) {
	_, err := prepareSession(sessionArgs{agent: "claude", gatewayURL: defaultGateway, dir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "opencode, pi") {
		t.Errorf("err = %v, want unknown agent listing opencode, pi", err)
	}
}

func TestParseSessionArgs(t *testing.T) {
	cases := []struct {
		args    []string
		dir     string
		model   string
		agent   string
		ro      []string
		command []string
		wantErr bool
	}{
		{args: nil, dir: "."},
		{args: []string{"app"}, dir: "app"},
		{args: []string{"--model", "m", "app"}, dir: "app", model: "m"},
		{args: []string{"app", "--model", "m"}, dir: "app", model: "m"},
		{args: []string{"--ro", "a", "app", "--ro", "b"}, dir: "app", ro: []string{"a", "b"}},
		{args: []string{"app", "--", "sh", "-c", "x"}, dir: "app", command: []string{"sh", "-c", "x"}},
		{args: []string{"--", "opencode", "run", "hi"}, dir: ".", command: []string{"opencode", "run", "hi"}},
		{args: []string{"--model", "m", "--", "app"}, dir: ".", model: "m", command: []string{"app"}},
		{args: []string{"app", "--agent", "pi"}, dir: "app", agent: "pi"},
		{args: []string{"a", "b"}, wantErr: true},
		{args: []string{"--nope"}, wantErr: true},
	}
	for _, c := range cases {
		sa, err := parseSessionArgs("run", c.args, nil)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: no error", c.args)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.args, err)
			continue
		}
		if c.agent == "" {
			c.agent = defaultAgent
		}
		if sa.agent != c.agent || sa.dir != c.dir || sa.model != c.model || !slices.Equal(sa.ro, c.ro) || !slices.Equal(sa.command, c.command) {
			t.Errorf("%q: got agent=%q dir=%q model=%q ro=%q command=%q", c.args, sa.agent, sa.dir, sa.model, sa.ro, sa.command)
		}
	}
}

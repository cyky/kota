package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localllm/internal/opencode"
	"localllm/internal/sandbox"
)

// TestSessionMountsNoHostAgentConfig builds a session against a fake HOME
// that has host agent config and state, and checks none of it is mounted.
func TestSessionMountsNoHostAgentConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("LOCALLLM_ALLOWED_ROOT", "")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	hostDirs := []string{".config/opencode", ".local/share/opencode", ".local/state/opencode", ".opencode", ".pi"}
	for _, d := range append(hostDirs, "Projects/app") {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	home, _ = filepath.EvalSymlinks(home)

	sess, err := prepareSession(sessionArgs{
		gatewayURL: defaultGateway,
		dir:        filepath.Join(home, "Projects/app"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.cleanup)

	agent := sess.spec.Services[sandbox.AgentService]
	var config, state bool
	for _, v := range agent.Volumes {
		for _, d := range hostDirs {
			if v.Source == filepath.Join(home, d) || strings.HasPrefix(v.Source, filepath.Join(home, d)+"/") {
				t.Errorf("host agent directory mounted: %+v", v)
			}
		}
		switch v.Target {
		case agentConfigPath:
			config = true
			if !v.ReadOnly || !strings.HasPrefix(v.Source, sess.sessionDir+"/") {
				t.Errorf("agent config mount = %+v, want read-only from the session dir", v)
			}
		case sandbox.StateDir:
			state = true
			if v.Type != "volume" {
				t.Errorf("state mount = %+v, want a named volume", v)
			}
		}
	}
	if !config || !state {
		t.Errorf("config mounted = %v, state mounted = %v in %+v", config, state, agent.Volumes)
	}
	want := sandbox.StateVolume(filepath.Join(home, "Projects/app"), opencode.Agent)
	if got := sess.spec.Volumes["state"].Name; got != want {
		t.Errorf("state volume = %q, want %q", got, want)
	}
	if agent.Environment["OPENCODE_DISABLE_PROJECT_CONFIG"] != "1" {
		t.Error("project opencode config is not disabled")
	}
}

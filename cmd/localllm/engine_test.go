package main

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"localllm/internal/engine"
	"localllm/internal/opencode"
	"localllm/internal/pi"
	"localllm/internal/sandbox"
)

// fakeEngine puts a stub QEMU on PATH and, unless missing says otherwise,
// a fake Engine base disk ("base") and prepped archives ("archives") for
// project into the fake HOME's cache.
func fakeEngine(t *testing.T, project string, images []string, missing ...string) engine.Layout {
	t.Helper()
	bin := t.TempDir()
	m, err := engine.MachineFor(engineHost())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, m.Binary), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	layout, err := engineLayout()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(missing, "base") {
		dir := layout.BaseDir()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{engine.BaseDisk, engine.BaseKernel, engine.BaseInitrd} {
			if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		def, _ := engine.BaseDefinition(engineHost().Arch)
		info, _ := json.Marshal(engine.BaseInfo{Definition: def, Arch: engineHost().Arch, Root: "PARTUUID=0b6ff0a5-3d1e-4e57-a02c-4ad3c2a3b8a1"})
		if err := os.WriteFile(filepath.Join(dir, engine.BaseFile), info, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Contains(missing, "archives") {
		dir := layout.ArchiveDir(project)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		var rec engine.Record
		for _, ref := range images {
			rec.Archives = append(rec.Archives, engine.Archive{Ref: ref, Digest: "sha256:" + strings.Repeat("a", 64), File: engine.ArchiveFile(ref)})
		}
		if err := rec.Save(dir); err != nil {
			t.Fatal(err)
		}
	}
	return layout
}

const engineYAML = "engine:\n  images: [postgres:16-alpine]\n"

// prepareShellSession prepares a session as shell does, which starts on a
// broken manifest with fallback dependency volumes.
func prepareShellSession(sa sessionArgs) (*session, error) {
	paths, err := resolveSessionPaths(sa)
	if err != nil {
		return nil, err
	}
	depPlan, err := sessionDeps(paths.Project)
	if err != nil {
		return nil, err
	}
	return prepareSession(sa, sandbox.ModeRun, paths, depPlan)
}

func TestShellEngineSession(t *testing.T) {
	project := fakeHome(t, map[string]string{engine.ConfigFile: engineYAML})
	t.Setenv("LOCALLLM_GATEWAY_KEY", "")
	t.Setenv("LOCALLLM_GATEWAY_KEY_FILE", "")
	fakeEngine(t, project, []string{"postgres:16-alpine"})
	sa := sessionArgs{agent: defaultAgent, gatewayURL: defaultGateway, dir: project, shell: true, engine: true}
	sess, err := prepareTestSession(sa, sandbox.ModeRun)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.cleanup)
	if sess.engine == nil {
		t.Fatal("no Engine VM for an engine project")
	}
	if _, ok := sess.spec.Services[sandbox.EngineService]; !ok {
		t.Error("no engine forwarder")
	}
	agent := sess.spec.Services[sandbox.AgentService]
	if agent.Environment["DOCKER_HOST"] != "tcp://engine:2376" || agent.Environment["TESTCONTAINERS_RYUK_DISABLED"] != "true" {
		t.Errorf("env = %v", agent.Environment)
	}
	if !slices.Contains(sess.mounts.ReadOnly, sandbox.EngineCertDir) {
		t.Error("probes don't check the client certificates are read-only")
	}
	// Only the client side is mounted; the server key stays on the host.
	for _, v := range agent.Volumes {
		if strings.Contains(v.Source, "engine-server") {
			t.Errorf("server TLS material mounted: %+v", v)
		}
	}
	for _, f := range []string{"ca.pem", "cert.pem", "key.pem"} {
		if _, err := os.Stat(filepath.Join(sess.sessionDir, "engine-client", f)); err != nil {
			t.Error(err)
		}
	}
	if !strings.HasPrefix(sess.engine.vm.Certs.Key, filepath.Join(sess.sessionDir, "engine-server")+"/") {
		t.Errorf("server key at %s", sess.engine.vm.Certs.Key)
	}
}

func TestEngineOnlyWhenWantedAndDeclared(t *testing.T) {
	t.Setenv("LOCALLLM_GATEWAY_KEY", "")
	t.Setenv("LOCALLLM_GATEWAY_KEY_FILE", "")
	t.Run("not wanted", func(t *testing.T) {
		project := fakeHome(t, map[string]string{engine.ConfigFile: engineYAML})
		fakeEngine(t, project, []string{"postgres:16-alpine"})
		sess, err := prepareTestSession(sessionArgs{agent: defaultAgent, gatewayURL: defaultGateway, dir: project}, sandbox.ModeRun)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(sess.cleanup)
		if sess.engine != nil || sess.spec.Services[sandbox.EngineService].Image != "" {
			t.Error("engine without being asked for")
		}
	})
	t.Run("no engine section", func(t *testing.T) {
		project := fakeHome(t, map[string]string{"README.md": "x"})
		sess, err := prepareTestSession(sessionArgs{agent: defaultAgent, gatewayURL: defaultGateway, dir: project, shell: true, engine: true}, sandbox.ModeRun)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(sess.cleanup)
		if sess.engine != nil {
			t.Error("engine for a project without an engine section")
		}
		if _, ok := sess.spec.Services[sandbox.AgentService].Environment["DOCKER_HOST"]; ok {
			t.Error("DOCKER_HOST without an engine")
		}
	})
	t.Run("broken config without engine history", func(t *testing.T) {
		project := fakeHome(t, map[string]string{engine.ConfigFile: "prep: [\n"})
		sess, err := prepareShellSession(sessionArgs{agent: defaultAgent, gatewayURL: defaultGateway, dir: project, shell: true, engine: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(sess.cleanup)
		if sess.engine != nil {
			t.Error("engine from a broken config")
		}
	})
}

func TestRunEngineSession(t *testing.T) {
	t.Setenv("LOCALLLM_GATEWAY_KEY", "")
	t.Setenv("LOCALLLM_GATEWAY_KEY_FILE", "")
	for _, agent := range []string{opencode.Agent, pi.Agent} {
		t.Run(agent, func(t *testing.T) {
			project := fakeHome(t, map[string]string{engine.ConfigFile: engineYAML})
			fakeEngine(t, project, []string{"postgres:16-alpine"})
			sa, err := parseOfflineArgs("run", []string{"--agent", agent, project}, nil)
			if err != nil {
				t.Fatal(err)
			}
			sess, err := prepareTestSession(sa, sandbox.ModeRun)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(sess.cleanup)
			if sess.engine == nil || sess.engineOff {
				t.Fatalf("run of an engine project: engine %v, off %v", sess.engine != nil, sess.engineOff)
			}
			env := sess.spec.Services[sandbox.AgentService].Environment
			if env["DOCKER_HOST"] != "tcp://engine:2376" || env[sandbox.EngineEnv] != sandbox.EngineAvailable {
				t.Errorf("env = %v", env)
			}
			if _, ok := sess.spec.Services[sandbox.EngineService]; !ok {
				t.Error("no engine forwarder")
			}
			if sess.spec.Services[sandbox.AgentService].Command[0] != agent {
				t.Errorf("command = %q", sess.spec.Services[sandbox.AgentService].Command)
			}
		})
	}
}

// TestNoEngine checks --no-engine starts run and shell of an engine project
// without the VM, even when it couldn't start, and tells the agent; and
// that it changes nothing for a project without an engine.
func TestNoEngine(t *testing.T) {
	t.Setenv("LOCALLLM_GATEWAY_KEY", "")
	t.Setenv("LOCALLLM_GATEWAY_KEY_FILE", "")
	for _, cmd := range []string{"run", "shell"} {
		t.Run(cmd, func(t *testing.T) {
			// No base disk and nothing prepped: the VM couldn't start.
			project := fakeHome(t, map[string]string{engine.ConfigFile: engineYAML})
			fakeEngine(t, project, nil, "base", "archives")
			sa, err := parseOfflineArgs(cmd, []string{"--no-engine", project}, nil)
			if err != nil {
				t.Fatal(err)
			}
			sa.shell = cmd == "shell"
			sess, err := prepareShellSession(sa)
			if err != nil {
				t.Fatalf("--no-engine refused: %v", err)
			}
			t.Cleanup(sess.cleanup)
			if sess.engine != nil || !sess.engineOff {
				t.Fatalf("engine %v, off %v", sess.engine != nil, sess.engineOff)
			}
			env := sess.spec.Services[sandbox.AgentService].Environment
			if env[sandbox.EngineEnv] != sandbox.EngineUnavailable {
				t.Errorf("%s = %q", sandbox.EngineEnv, env[sandbox.EngineEnv])
			}
			if _, ok := env["DOCKER_HOST"]; ok {
				t.Error("DOCKER_HOST with --no-engine")
			}
			if _, ok := sess.spec.Services[sandbox.EngineService]; ok {
				t.Error("engine forwarder with --no-engine")
			}
			var out strings.Builder
			sess.printWarnings(&out)
			if !strings.Contains(out.String(), "--no-engine: the Engine VM is off") {
				t.Errorf("warnings = %q", out.String())
			}
		})
	}
	t.Run("broken config of an engine project", func(t *testing.T) {
		project := fakeHome(t, map[string]string{engine.ConfigFile: "engine: [\n"})
		fakeEngine(t, project, []string{"postgres:16-alpine"})
		sa, err := parseOfflineArgs("run", []string{"--no-engine", project}, nil)
		if err != nil {
			t.Fatal(err)
		}
		sess, err := prepareShellSession(sa)
		if err != nil {
			t.Fatalf("--no-engine refused: %v", err)
		}
		t.Cleanup(sess.cleanup)
		if !sess.engineOff {
			t.Error("a prepped engine project with a broken config isn't marked off")
		}
	})
	t.Run("no engine section", func(t *testing.T) {
		project := fakeHome(t, map[string]string{"README.md": "x"})
		specs := map[bool]string{}
		for _, noEngine := range []bool{false, true} {
			args := []string{project}
			if noEngine {
				args = append(args, "--no-engine")
			}
			sa, err := parseOfflineArgs("run", args, nil)
			if err != nil {
				t.Fatal(err)
			}
			sess, err := prepareTestSession(sa, sandbox.ModeRun)
			if err != nil {
				t.Fatal(err)
			}
			if sess.engineOff || sess.engineNote() != "" {
				t.Errorf("--no-engine=%v: off %v, note %q", noEngine, sess.engineOff, sess.engineNote())
			}
			data, _ := json.Marshal(sess.spec)
			specs[noEngine] = strings.ReplaceAll(string(data), sess.sessionDir, "SESSION")
			sess.cleanup()
		}
		if specs[false] != specs[true] {
			t.Errorf("--no-engine changed the spec of a project without an engine:\n%s\n%s", specs[false], specs[true])
		}
	})
}

func TestParseOfflineArgs(t *testing.T) {
	for _, cmd := range []string{"run", "shell"} {
		sa, err := parseOfflineArgs(cmd, []string{"app"}, nil)
		if err != nil || !sa.engine {
			t.Errorf("%s: engine = %v, err = %v; want the Engine VM by default", cmd, sa.engine, err)
		}
		sa, err = parseOfflineArgs(cmd, []string{"app", "--no-engine"}, nil)
		if err != nil || sa.engine || sa.dir != "app" {
			t.Errorf("%s --no-engine: engine = %v, dir = %q, err = %v", cmd, sa.engine, sa.dir, err)
		}
	}
	if _, err := parseSessionArgs("check", []string{"--no-engine"}, nil); err == nil {
		t.Error("check accepted --no-engine")
	}
}

// TestAgentEngineInstructions prepares a run of each agent in each engine
// state and checks what the agent is told, in the file it reads
// instructions from: opencode's global AGENTS.md, pi's APPEND_SYSTEM.md.
// Without an engine, nothing is mounted there.
func TestAgentEngineInstructions(t *testing.T) {
	t.Setenv("LOCALLLM_GATEWAY_KEY", "")
	t.Setenv("LOCALLLM_GATEWAY_KEY_FILE", "")
	notes := map[string]string{
		opencode.Agent: opencodeInstructionsPath,
		pi.Agent:       piAgentDir + "/" + pi.AppendSystem,
	}
	states := []struct {
		name string
		yaml string
		args []string
		want string // "" means no instructions
	}{
		{name: "available", yaml: engineYAML, want: sandbox.EngineAvailable},
		{name: "--no-engine", yaml: engineYAML, args: []string{"--no-engine"}, want: sandbox.EngineUnavailable},
		{name: "no engine section", yaml: "prep:\n  dirs: [.venv]\n"},
	}
	for _, name := range []string{opencode.Agent, pi.Agent} {
		for _, st := range states {
			t.Run(name+"/"+st.name, func(t *testing.T) {
				project := fakeHome(t, map[string]string{engine.ConfigFile: st.yaml})
				fakeEngine(t, project, []string{"postgres:16-alpine"})
				sa, err := parseOfflineArgs("run", append([]string{"--agent", name, project}, st.args...), nil)
				if err != nil {
					t.Fatal(err)
				}
				sess, err := prepareTestSession(sa, sandbox.ModeRun)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(sess.cleanup)
				agent := sess.spec.Services[sandbox.AgentService]
				var v sandbox.Volume
				var mounted bool
				for _, vol := range agent.Volumes {
					if vol.Target == notes[name] {
						v, mounted = vol, true
					}
				}
				if st.want == "" {
					if mounted {
						t.Errorf("note mounted without an engine: %+v", v)
					}
					if name == opencode.Agent && len(agent.Tmpfs) != 1 {
						t.Errorf("tmpfs = %q, want only HOME", agent.Tmpfs)
					}
					return
				}
				if !mounted || !v.ReadOnly || !strings.HasPrefix(v.Source, sess.sessionDir+"/") {
					t.Fatalf("note mount = %+v (found %v), want read-only from the session dir", v, mounted)
				}
				if !slices.Contains(sess.mounts.ReadOnly, notes[name]) {
					t.Error("probes don't check the note is read-only")
				}
				// The agent still writes its own state next to the note.
				dir := path.Dir(notes[name])
				if !slices.ContainsFunc(agent.Tmpfs, func(s string) bool { return strings.HasPrefix(s, dir+":") }) {
					t.Errorf("tmpfs = %q, want %s writable", agent.Tmpfs, dir)
				}
				data, err := os.ReadFile(v.Source)
				if err != nil {
					t.Fatal(err)
				}
				note := string(data)
				if !strings.Contains(note, sandbox.EngineEnv+"="+st.want) || agent.Environment[sandbox.EngineEnv] != st.want {
					t.Errorf("note = %q, env %s = %q; want %s", note, sandbox.EngineEnv, agent.Environment[sandbox.EngineEnv], st.want)
				}
				if st.want == sandbox.EngineUnavailable && !strings.Contains(note, "not run") {
					t.Errorf("note doesn't say to report container tests as not run: %q", note)
				}
			})
		}
	}
}

func TestEngineSessionRefusals(t *testing.T) {
	t.Setenv("LOCALLLM_GATEWAY_KEY", "")
	t.Setenv("LOCALLLM_GATEWAY_KEY_FILE", "")
	cases := []struct {
		name    string
		yaml    string
		missing []string
		want    string
	}{
		{name: "no base disk", yaml: engineYAML, missing: []string{"base"}, want: "localllm build"},
		{name: "not prepped", yaml: engineYAML, missing: []string{"archives"}, want: "localllm prep"},
		{name: "new image not prepped", yaml: "engine:\n  images: [postgres:16-alpine, redis:7]\n", want: "redis:7"},
		{name: "broken config of an engine project", yaml: "engine: [\n", want: "can't start"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			project := fakeHome(t, map[string]string{engine.ConfigFile: c.yaml})
			fakeEngine(t, project, []string{"postgres:16-alpine"}, c.missing...)
			_, err := prepareShellSession(sessionArgs{agent: defaultAgent, gatewayURL: defaultGateway, dir: project, shell: true, engine: true})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
			cache, _ := os.UserCacheDir()
			if entries, _ := os.ReadDir(filepath.Join(cache, "localllm", "sessions")); len(entries) > 0 {
				t.Errorf("session dir left: %v", entries)
			}
		})
	}
}

func TestCheckShowsEngine(t *testing.T) {
	t.Setenv("LOCALLLM_GATEWAY_KEY", "")
	t.Setenv("LOCALLLM_GATEWAY_KEY_FILE", "")
	check := sessionArgs{agent: defaultAgent, gatewayURL: defaultGateway, engine: true, engineOptional: true}
	cases := []struct {
		name, yaml string
		missing    []string
		want       []string
		wantErr    bool
	}{
		{name: "no engine", yaml: "prep:\n  dirs: [.venv]\n", want: []string{"engine: none (no engine section"}},
		{name: "ready to start", yaml: engineYAML, want: []string{"image postgres:16-alpine (sha256:aaaa"}},
		{name: "no base disk", yaml: engineYAML, missing: []string{"base"}, want: []string{"image postgres:16-alpine\n", "status: can't start", "localllm build"}, wantErr: true},
		{name: "not prepped", yaml: engineYAML, missing: []string{"archives"}, want: []string{"status: can't start", "localllm prep"}, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			project := fakeHome(t, map[string]string{engine.ConfigFile: c.yaml})
			fakeEngine(t, project, []string{"postgres:16-alpine"}, c.missing...)
			sa := check
			sa.dir = project
			sess, err := prepareShellSession(sa)
			if err != nil {
				t.Fatalf("check refused to prepare: %v", err)
			}
			t.Cleanup(sess.cleanup)
			var out strings.Builder
			err = printEngine(&out, sess)
			for _, want := range c.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output lacks %q:\n%s", want, out.String())
				}
			}
			if (err != nil) != c.wantErr {
				t.Errorf("err = %v, want error %v", err, c.wantErr)
			}
			if c.wantErr && sess.spec.Services[sandbox.EngineService].Image != "" {
				t.Error("engine forwarder without an Engine VM")
			}
		})
	}
}

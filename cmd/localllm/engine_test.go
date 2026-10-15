package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"localllm/internal/engine"
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
	t.Run("run", func(t *testing.T) {
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

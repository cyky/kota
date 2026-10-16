package enginesession

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"localllm/internal/engine"
	"localllm/internal/sandbox"
)

var host = engine.Host{OS: runtime.GOOS, Arch: runtime.GOARCH}

// fixture is a project with an engine section, a stub QEMU on PATH, and,
// unless missing names them, a built Engine base disk ("base") and prepped
// archives ("archives") for images.
func fixture(t *testing.T, yaml string, images []string, missing ...string) (Options, string) {
	t.Helper()
	bin := t.TempDir()
	if !slices.Contains(missing, "qemu") {
		m, err := engine.MachineFor(host)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bin, m.Binary), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	project := t.TempDir()
	if yaml != "" {
		if err := os.WriteFile(filepath.Join(project, engine.ConfigFile), []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	layout := engine.Layout{Root: t.TempDir()}
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
		def, _ := engine.BaseDefinition(host.Arch)
		info, _ := json.Marshal(engine.BaseInfo{Definition: def, Arch: host.Arch, Root: "PARTUUID=0b6ff0a5-3d1e-4e57-a02c-4ad3c2a3b8a1"})
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
	return Options{Layout: layout, Host: host, ProbeSource: "probe:latest", HostAlias: "host.test"}, project
}

const yaml = "engine:\n  images: [postgres:16-alpine]\n  cpus: 3\n  memory: 3G\n"

func TestPrepare(t *testing.T) {
	o, project := fixture(t, yaml, []string{"postgres:16-alpine"})
	sessionDir := t.TempDir()
	e, err := Prepare(o, project, sessionDir)
	if err != nil || e == nil {
		t.Fatalf("Prepare = %v, %v", e, err)
	}
	if !slices.Equal(e.Images(), []string{"postgres:16-alpine"}) || e.Warning() != "" {
		t.Errorf("images %q, warning %q", e.Images(), e.Warning())
	}
	vm := e.VM()
	if vm.CPUs != 3 || vm.MemoryMiB != 3072 || vm.Pidfile != o.Layout.Pidfile(project) {
		t.Errorf("vm = %+v", vm)
	}
	// The server side stays in the session dir for fw_cfg; only the client
	// side is the route's, for the Sandbox.
	for _, f := range []string{vm.Certs.CA, vm.Certs.Cert, vm.Certs.Key} {
		if !strings.HasPrefix(f, filepath.Join(sessionDir, "engine-server")+"/") {
			t.Errorf("server file %s outside engine-server", f)
		}
	}
	r := e.Route()
	if r.Host != "host.test" || r.APIPort != engine.GuestAPIPort || r.CertDir != filepath.Join(sessionDir, "engine-client") {
		t.Errorf("route = %+v", r)
	}
	for _, f := range []string{"ca.pem", "cert.pem", "key.pem"} {
		if _, err := os.Stat(filepath.Join(r.CertDir, f)); err != nil {
			t.Error(err)
		}
	}
	// The route forwards the API and the published range, each from the
	// host port QEMU forwards to the VM's own port number.
	if len(r.Forwards) != 1+len(engine.PublishedPorts()) || r.Forwards[0].Listen != engine.GuestAPIPort || r.Forwards[0].Target != vm.Ports.API {
		t.Errorf("forwards = %+v", r.Forwards)
	}
	targets := e.ProbeTargets(sandbox.Gateway{Host: "host.docker.internal", Port: 8080})
	if targets.Image != ProbeImage || !slices.Contains(targets.HostPorts, vm.Ports.API) || !slices.Contains(targets.HostPorts, vm.Ports.Published[0]) {
		t.Errorf("probe targets = %+v", targets)
	}
	if e.PID() != 0 {
		t.Error("PID before Start")
	}
}

func TestPrepareNoEngine(t *testing.T) {
	for name, yaml := range map[string]string{"no file": "", "no section": "prep:\n  dirs: [.venv]\n", "broken, never prepped": "engine: [\n"} {
		t.Run(name, func(t *testing.T) {
			o, project := fixture(t, yaml, nil, "archives")
			e, err := Prepare(o, project, t.TempDir())
			if e != nil || err != nil {
				t.Errorf("Prepare = %v, %v; want no Engine VM", e, err)
			}
			if Declared(o.Layout, project) {
				t.Error("declared")
			}
		})
	}
}

func TestPrepareRefusals(t *testing.T) {
	cases := []struct {
		name, yaml string
		missing    []string
		want       string
	}{
		{name: "no QEMU", yaml: yaml, missing: []string{"qemu"}, want: "declares engine images, but"},
		{name: "no base disk", yaml: yaml, missing: []string{"base"}, want: "localllm build"},
		{name: "not prepped", yaml: yaml, missing: []string{"archives"}, want: "localllm prep"},
		{name: "new image not prepped", yaml: "engine:\n  images: [postgres:16-alpine, redis:7]\n", want: "redis:7"},
		{name: "broken config of a prepped project", yaml: "engine: [\n", want: "can't start without knowing its images"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, project := fixture(t, c.yaml, []string{"postgres:16-alpine"}, c.missing...)
			e, err := Prepare(o, project, t.TempDir())
			if e != nil || err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Prepare = %v, %v; want an error mentioning %q", e, err, c.want)
			}
			if !Declared(o.Layout, project) {
				t.Error("a project whose Engine VM can't start isn't declared")
			}
		})
	}
}

// Package enginesession is a project's Engine VM for one Session. Prepare
// works it out from the project's engine section, the Engine base disk and
// the prepped archives, with TLS certificates made for the session. The
// session then starts it next to the Sandbox, waits for its Docker API with
// the Engine images loaded, runs its probes through the Sandbox, and stops
// it.
//
// The pure parts (the QEMU command line, certificates, image records) are
// in package engine, and the QEMU process in package enginevm.
package enginesession

import (
	"fmt"
	"io"
	"path/filepath"
	"time"

	"kota/internal/check"
	"kota/internal/docker"
	"kota/internal/engine"
	"kota/internal/enginevm"
	"kota/internal/probe"
	"kota/internal/sandbox"
)

// ProbeImage is the tag the probe image gets inside the Engine VM.
const ProbeImage = "kota-engine-probe:latest"

// Options are what every Engine VM of this kota shares.
type Options struct {
	Layout engine.Layout
	Host   engine.Host
	// ProbeSource is the image on the host's Docker that is saved and loaded
	// into the VM for its probes: the VM can't download one.
	ProbeSource string
	// HostAlias is the host as the engine forwarder reaches it.
	HostAlias string
	// BootTimeout bounds the boot to a ready Docker API.
	BootTimeout time.Duration
}

// Engine is one session's Engine VM.
type Engine struct {
	opts       Options
	vm         engine.VM
	cmd        engine.Command
	certs      engine.Certs
	route      *sandbox.EngineRoute
	archiveDir string
	archives   []engine.Archive
	warning    string
	probeTar   string
	running    *enginevm.VM
	started    time.Time
}

// Prepare works out project's Engine VM for a session whose files live in
// sessionDir: the server TLS material for the VM in engine-server, the
// client side for the Sandbox in engine-client. It returns nil when the
// project has no Engine VM, and an error with the fix when its VM can't
// start: no QEMU, no Engine base disk, images not prepped, or a broken
// engine section in a project that was prepped with one.
func Prepare(o Options, project, sessionDir string) (*Engine, error) {
	cfg, err := config(o.Layout, project)
	if cfg == nil || err != nil {
		return nil, err
	}
	m, err := engine.MachineFor(o.Host)
	if err != nil {
		return nil, err
	}
	if _, err := enginevm.FindQEMU(m); err != nil {
		return nil, fmt.Errorf("%s declares engine images, but %w", engine.ConfigFile, err)
	}
	base, warning, err := o.Layout.LoadBase(o.Host.Arch)
	if err != nil {
		return nil, err
	}
	archiveDir := o.Layout.ArchiveDir(project)
	rec, err := engine.LoadRecord(archiveDir)
	if err != nil {
		return nil, err
	}
	archives, err := rec.Select(cfg.Images)
	if err != nil {
		return nil, fmt.Errorf("%w; run 'kota prep %s'", err, project)
	}
	ports, err := enginevm.FreePorts(1 + len(engine.PublishedPorts()))
	if err != nil {
		return nil, err
	}
	certs, err := engine.NewCerts(time.Now(), []string{sandbox.EngineHost, "127.0.0.1"})
	if err != nil {
		return nil, err
	}
	server, err := certs.WriteServer(filepath.Join(sessionDir, "engine-server"))
	if err != nil {
		return nil, err
	}
	vm := engine.VM{
		Project:   project,
		Host:      o.Host,
		CPUs:      cfg.CPUs,
		MemoryMiB: cfg.MemoryMiB,
		Base:      base,
		Certs:     server,
		Ports:     engine.Ports{API: ports[0], Published: ports[1:]},
		Serial:    filepath.Join(sessionDir, "engine-console.log"),
		Pidfile:   o.Layout.Pidfile(project),
	}
	cmd, err := engine.Build(vm)
	if err != nil {
		return nil, err
	}
	e := &Engine{opts: o, vm: vm, cmd: cmd, certs: certs, archiveDir: archiveDir, archives: archives, warning: warning,
		probeTar: filepath.Join(sessionDir, "engine-probe.tar")}
	if e.route, err = e.writeRoute(filepath.Join(sessionDir, "engine-client")); err != nil {
		return nil, err
	}
	return e, nil
}

// declared reports whether project has an Engine VM, which a session
// without it tells the agent about. A broken engine section of a project
// that was prepped with one counts.
func declared(layout engine.Layout, project string) bool {
	cfg, err := config(layout, project)
	return cfg != nil || err != nil
}

// config reads project's engine section: nil for a project without an
// Engine VM.
func config(layout engine.Layout, project string) (*engine.Config, error) {
	cfg, err := engine.Read(project)
	if err != nil {
		// A broken kota.yaml shouldn't block a project that never used
		// an engine, but an engine project can't tell which images to load.
		if rec, rerr := engine.LoadRecord(layout.ArchiveDir(project)); rerr == nil && rec == nil {
			return nil, nil
		}
		return nil, fmt.Errorf("%w; the Engine VM can't start without knowing its images", err)
	}
	return cfg, nil
}

// writeRoute writes the client certificate into dir and returns the
// Sandbox's route to the VM. The agent sees the VM's own port numbers.
func (e *Engine) writeRoute(dir string) (*sandbox.EngineRoute, error) {
	if err := e.certs.WriteClient(dir); err != nil {
		return nil, err
	}
	fwds, err := e.vm.Ports.Forwards()
	if err != nil {
		return nil, err
	}
	r := &sandbox.EngineRoute{Host: e.opts.HostAlias, APIPort: engine.GuestAPIPort, CertDir: dir}
	for _, f := range fwds {
		r.Forwards = append(r.Forwards, sandbox.PortForward{Listen: f.Guest, Target: f.Host})
	}
	return r, nil
}

// Route is the Sandbox's route to the VM, for its spec.
func (e *Engine) Route() *sandbox.EngineRoute { return e.route }

// Archives are the Engine images the VM gets, as prep recorded them.
func (e *Engine) Archives() []engine.Archive { return e.archives }

// Images are the declared Engine images.
func (e *Engine) Images() []string {
	var refs []string
	for _, a := range e.archives {
		refs = append(refs, a.Ref)
	}
	return refs
}

// Warning says the Engine base disk should be rebuilt, or is "".
func (e *Engine) Warning() string { return e.warning }

// VM is the VM's definition.
func (e *Engine) VM() engine.VM { return e.vm }

// Start boots the VM without waiting for it, so it boots while the Sandbox
// starts.
func (e *Engine) Start() error {
	vm, err := enginevm.Start(e.cmd, e.vm, e.certs.CA, e.certs.ClientCert, e.certs.ClientKey)
	if err != nil {
		return err
	}
	e.running, e.started = vm, time.Now()
	return nil
}

// Done is closed when the started VM exits.
func (e *Engine) Done() <-chan struct{} { return e.running.Done() }

// PID is the started VM's QEMU process, or 0.
func (e *Engine) PID() int {
	if e.running == nil {
		return 0
	}
	return e.running.PID()
}

// Ready waits for the started VM's Docker API and loads the Engine images.
// It also saves the probe image from the host while the VM boots.
func (e *Engine) Ready(w io.Writer) error {
	if err := docker.SaveImage(e.opts.ProbeSource, e.probeTar); err != nil {
		return fmt.Errorf("the Engine VM probes: %w; run 'kota build'", err)
	}
	if err := e.running.WaitReady(e.opts.BootTimeout); err != nil {
		return fmt.Errorf("%w\nIf this keeps happening, remove %s and run 'kota build' to rebuild the Engine base disk", err, filepath.Dir(e.vm.Base.Disk))
	}
	booted := time.Since(e.started)
	loadStart := time.Now()
	if err := e.running.Load(e.archiveDir, e.archives); err != nil {
		return fmt.Errorf("%w; run 'kota prep' to download the archives again", err)
	}
	fmt.Fprintf(w, "Engine VM ready: booted in %s, loaded %d image(s) in %s\n",
		booted.Round(100*time.Millisecond), len(e.archives), time.Since(loadStart).Round(100*time.Millisecond))
	return nil
}

// ProbeTargets are what the probes check for a Sandbox on gateway gw.
func (e *Engine) ProbeTargets(gw sandbox.Gateway) probe.EngineTargets {
	return probe.EngineTargets{
		Image:        ProbeImage,
		GuestAPIPort: engine.GuestAPIPort,
		HostAddrs:    check.EngineHostAddrs(),
		// The host side of the VM's own forwards, so the VM can't loop back
		// through the host.
		HostPorts:  check.EnginePorts(gw, []int{e.vm.Ports.API, e.vm.Ports.Published[0]}),
		EngineHost: sandbox.EngineHost,
		APIPort:    engine.GuestAPIPort,
		CertDir:    sandbox.EngineCertDir,
	}
}

// Probes loads the probe image into the ready VM and runs the probes for
// targets through the started Sandbox ds. They remove the image afterwards.
func (e *Engine) Probes(ds *docker.Session, targets probe.EngineTargets) ([]probe.Result, error) {
	if err := e.running.LoadTar(e.probeTar, ProbeImage); err != nil {
		return nil, err
	}
	return check.RunEngine(ds, targets)
}

// Stop stops the VM, if it was started.
func (e *Engine) Stop() {
	if e.running != nil {
		e.running.Stop()
	}
}

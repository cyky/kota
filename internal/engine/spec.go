package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

const (
	// GuestAPIPort is where dockerd listens with TLS inside the Engine VM.
	GuestAPIPort = 2376
	// FirstPublishedPort and LastPublishedPort are the range the Engine
	// base disk sets as net.ipv4.ip_local_port_range, so Docker publishes
	// container ports only there. Only this range and the API are
	// forwarded.
	FirstPublishedPort = 40000
	LastPublishedPort  = 40099

	// fwCfgPrefix names the fw_cfg entries the Engine base disk's
	// engine-tls service reads at boot.
	fwCfgPrefix = "opt/kota/"
)

// PublishedPorts returns the in-VM published port range, in order.
func PublishedPorts() []int {
	var ports []int
	for p := FirstPublishedPort; p <= LastPublishedPort; p++ {
		ports = append(ports, p)
	}
	return ports
}

// Host is the machine QEMU runs on, as runtime.GOOS and runtime.GOARCH.
type Host struct {
	OS, Arch string
}

// Machine is how QEMU runs a VM of the host's architecture.
type Machine struct {
	// Binary is the QEMU system emulator.
	Binary  string
	Machine string
	Accel   string
	// Console is the serial console device for the kernel command line.
	Console string
	// Platform is the container platform of the guest, for image pulls.
	Platform string
}

// MachineFor returns the QEMU setup for host. The guest always has the
// host's architecture, so the accelerator can run it.
func MachineFor(h Host) (Machine, error) {
	var m Machine
	switch h.Arch {
	case "arm64":
		m = Machine{Binary: "qemu-system-aarch64", Machine: "virt", Console: "ttyAMA0", Platform: "linux/arm64"}
	case "amd64":
		m = Machine{Binary: "qemu-system-x86_64", Machine: "q35", Console: "ttyS0", Platform: "linux/amd64"}
	default:
		return Machine{}, fmt.Errorf("the Engine VM doesn't support %s hosts", h.Arch)
	}
	switch h.OS {
	case "darwin":
		m.Accel = "hvf"
	case "linux":
		m.Accel = "kvm"
	default:
		return Machine{}, fmt.Errorf("the Engine VM doesn't support %s hosts", h.OS)
	}
	return m, nil
}

// Ports are the host ports, on 127.0.0.1, that a session's Engine VM is
// reached on.
type Ports struct {
	// API is forwarded to GuestAPIPort.
	API int
	// Published has one host port per PublishedPorts entry, in order.
	Published []int
}

// Forward is a port forward from the host to the Engine VM.
type Forward struct {
	Host, Guest int
}

// Forwards pairs p with the guest ports, the API first.
func (p Ports) Forwards() ([]Forward, error) {
	guest := PublishedPorts()
	if len(p.Published) != len(guest) {
		return nil, fmt.Errorf("engine ports: %d host ports for %d published ports", len(p.Published), len(guest))
	}
	fwds := []Forward{{Host: p.API, Guest: GuestAPIPort}}
	for i, g := range guest {
		fwds = append(fwds, Forward{Host: p.Published[i], Guest: g})
	}
	for _, f := range fwds {
		if f.Host < 1 || f.Host > 65535 {
			return nil, fmt.Errorf("engine ports: invalid host port %d", f.Host)
		}
	}
	return fwds, nil
}

// Base is the Engine base disk and the kernel it boots directly.
type Base struct {
	Disk, Kernel, Initrd string
	// Root is the kernel's root= value, e.g. PARTUUID=....
	Root string
}

// ServerCerts are the files holding the Engine VM's TLS material: the CA
// that signed the session's client certificate, and the server's own
// certificate and key.
type ServerCerts struct {
	CA, Cert, Key string
}

// VM describes one session's Engine VM.
type VM struct {
	// Project is the resolved project directory; it names the VM.
	Project   string
	Host      Host
	CPUs      int
	MemoryMiB int
	Base      Base
	Certs     ServerCerts
	Ports     Ports
	// Serial is the file the VM's console is written to.
	Serial string
	// Pidfile is where QEMU writes its PID, so a later command can stop a
	// VM whose kota died.
	Pidfile string
}

// Command is a program and its arguments.
type Command struct {
	Path string
	Args []string
}

// Name returns the VM name of a project directory. It derives from the
// same hash as the project's volumes: stable per path, different across
// paths.
func Name(project string) string {
	sum := sha256.Sum256([]byte(project))
	return "kota-engine-" + hex.EncodeToString(sum[:])[:32]
}

// Build returns the QEMU command line for vm. The VM has no host mounts:
// its only disk is the Engine base disk under -snapshot, so writes go to a
// throwaway layer. Its network is user-mode with restrict=on, so the guest
// can't open connections out, and the only forwards are the API and the
// published range, bound to 127.0.0.1. The TLS material is passed through
// fw_cfg. The kernel is booted directly, without firmware or boot loader.
func Build(vm VM) (Command, error) {
	m, err := MachineFor(vm.Host)
	if err != nil {
		return Command{}, err
	}
	fwds, err := vm.Ports.Forwards()
	if err != nil {
		return Command{}, err
	}
	if vm.CPUs < 1 || vm.MemoryMiB < 256 {
		return Command{}, fmt.Errorf("engine VM size: %d CPUs, %d MiB", vm.CPUs, vm.MemoryMiB)
	}
	if !rootArg.MatchString(vm.Base.Root) {
		return Command{}, fmt.Errorf("engine base disk: invalid root %q", vm.Base.Root)
	}
	netdev := "user,id=net0,restrict=on"
	for _, f := range fwds {
		netdev += fmt.Sprintf(",hostfwd=tcp:127.0.0.1:%d-:%d", f.Host, f.Guest)
	}
	args := []string{
		"-name", Name(vm.Project),
		"-nodefaults", "-no-user-config",
		"-machine", m.Machine,
		"-accel", m.Accel,
		"-cpu", "host",
		"-smp", strconv.Itoa(vm.CPUs),
		"-m", strconv.Itoa(vm.MemoryMiB),
		"-kernel", vm.Base.Kernel,
		"-initrd", vm.Base.Initrd,
		"-append", "root=" + vm.Base.Root + " ro console=" + m.Console,
		"-drive", "if=virtio,format=qcow2,file=" + optValue(vm.Base.Disk),
		"-snapshot",
		"-netdev", netdev,
		"-device", "virtio-net-pci,netdev=net0",
		"-fw_cfg", "name=" + fwCfgPrefix + "ca,file=" + optValue(vm.Certs.CA),
		"-fw_cfg", "name=" + fwCfgPrefix + "cert,file=" + optValue(vm.Certs.Cert),
		"-fw_cfg", "name=" + fwCfgPrefix + "key,file=" + optValue(vm.Certs.Key),
		"-display", "none",
		"-serial", "file:" + vm.Serial,
		"-monitor", "none",
		"-pidfile", vm.Pidfile,
	}
	return Command{Path: m.Binary, Args: args}, nil
}

// optValue escapes a value inside a QEMU option list, where a comma
// separates options and ",," is a literal comma.
func optValue(s string) string { return strings.ReplaceAll(s, ",", ",,") }

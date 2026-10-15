package engine

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func testVM() VM {
	ports := Ports{API: 23760}
	for i := range PublishedPorts() {
		ports.Published = append(ports.Published, 50000+i)
	}
	return VM{
		Project:   "/Users/me/Projects/foo",
		Host:      Host{OS: "darwin", Arch: "arm64"},
		CPUs:      2,
		MemoryMiB: 2048,
		Base:      Base{Disk: "/c/engine/base/disk.qcow2", Kernel: "/c/engine/base/vmlinuz", Initrd: "/c/engine/base/initrd.img", Root: "PARTUUID=0b6ff0a5-3d1e-4e57-a02c-4ad3c2a3b8a1"},
		Certs:     ServerCerts{CA: "/s/ca.pem", Cert: "/s/server.pem", Key: "/s/server-key.pem"},
		Ports:     ports,
		Serial:    "/s/serial.log",
		Pidfile:   "/c/engine/projects/x/qemu.pid",
	}
}

// opts returns the values of every occurrence of flag.
func opts(args []string, flag string) []string {
	var out []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func build(t *testing.T, vm VM) Command {
	t.Helper()
	cmd, err := Build(vm)
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}

func TestNoHostMounts(t *testing.T) {
	cmd := build(t, testVM())
	for _, flag := range []string{"-virtfs", "-fsdev", "-hda", "-hdb", "-cdrom", "-blockdev", "-chardev", "-qmp"} {
		if len(opts(cmd.Args, flag)) > 0 {
			t.Errorf("%s in %q", flag, cmd.Args)
		}
	}
	drives := opts(cmd.Args, "-drive")
	if len(drives) != 1 || !strings.Contains(drives[0], "file=/c/engine/base/disk.qcow2") {
		t.Errorf("drives = %q, want only the base disk", drives)
	}
	for _, d := range opts(cmd.Args, "-device") {
		if strings.Contains(d, "virtio-9p") || strings.Contains(d, "vhost-user-fs") {
			t.Errorf("shared folder device %q", d)
		}
	}
}

func TestThrowawayDiskLayer(t *testing.T) {
	cmd := build(t, testVM())
	if !slices.Contains(cmd.Args, "-snapshot") {
		t.Errorf("no -snapshot in %q", cmd.Args)
	}
}

func TestNetworkRestrictedWithOnlyAgentPorts(t *testing.T) {
	vm := testVM()
	cmd := build(t, vm)
	netdevs := opts(cmd.Args, "-netdev")
	if len(netdevs) != 1 {
		t.Fatalf("netdevs = %q, want one", netdevs)
	}
	parts := strings.Split(netdevs[0], ",")
	if parts[0] != "user" || !slices.Contains(parts, "restrict=on") {
		t.Errorf("netdev %q is not user-mode with restrict=on", netdevs[0])
	}
	for _, p := range parts {
		if strings.HasPrefix(p, "guestfwd") || strings.HasPrefix(p, "smb") || strings.HasPrefix(p, "tftp") || strings.HasPrefix(p, "bootfile") {
			t.Errorf("netdev option %q", p)
		}
	}
	fwd := regexp.MustCompile(`^hostfwd=tcp:127\.0\.0\.1:(\d+)-:(\d+)$`)
	guest := map[string]string{}
	for _, p := range parts {
		if !strings.HasPrefix(p, "hostfwd") {
			continue
		}
		m := fwd.FindStringSubmatch(p)
		if m == nil {
			t.Errorf("forward %q isn't tcp on 127.0.0.1", p)
			continue
		}
		guest[m[2]] = m[1]
	}
	want := map[string]string{"2376": "23760"}
	for i, g := range PublishedPorts() {
		want[strconv.Itoa(g)] = strconv.Itoa(vm.Ports.Published[i])
	}
	if len(guest) != len(want) {
		t.Errorf("%d forwards, want %d", len(guest), len(want))
	}
	for g, h := range want {
		if guest[g] != h {
			t.Errorf("guest port %s forwarded from %q, want %s", g, guest[g], h)
		}
	}
	if len(opts(cmd.Args, "-nic"))+len(opts(cmd.Args, "-net")) > 0 {
		t.Errorf("extra network in %q", cmd.Args)
	}
	if !slices.Contains(cmd.Args, "-nodefaults") {
		t.Error("default devices (and a default NIC) not disabled")
	}
}

func TestPortsMustCoverTheRange(t *testing.T) {
	vm := testVM()
	vm.Ports.Published = vm.Ports.Published[:10]
	if _, err := Build(vm); err == nil {
		t.Error("built with a partial port range")
	}
	vm = testVM()
	vm.Ports.API = 0
	if _, err := Build(vm); err == nil {
		t.Error("built with API port 0")
	}
}

func TestNameStablePerProject(t *testing.T) {
	a := build(t, testVM())
	b := build(t, testVM())
	other := testVM()
	other.Project = "/Users/me/Projects/bar"
	c := build(t, other)
	name := func(cmd Command) string { return opts(cmd.Args, "-name")[0] }
	if name(a) != name(b) {
		t.Errorf("names differ for one project: %s, %s", name(a), name(b))
	}
	if name(a) == name(c) {
		t.Errorf("two projects share the name %s", name(a))
	}
	if !strings.HasPrefix(name(a), "localllm-engine-") {
		t.Errorf("name = %s", name(a))
	}
}

func TestSizeApplied(t *testing.T) {
	vm := testVM()
	vm.CPUs, vm.MemoryMiB = 4, 3072
	cmd := build(t, vm)
	if got := opts(cmd.Args, "-smp"); !slices.Equal(got, []string{"4"}) {
		t.Errorf("-smp = %q", got)
	}
	if got := opts(cmd.Args, "-m"); !slices.Equal(got, []string{"3072"}) {
		t.Errorf("-m = %q", got)
	}
}

func TestAcceleratorPerHost(t *testing.T) {
	cases := []struct {
		host                   Host
		binary, accel, console string
	}{
		{Host{"darwin", "arm64"}, "qemu-system-aarch64", "hvf", "ttyAMA0"},
		{Host{"linux", "arm64"}, "qemu-system-aarch64", "kvm", "ttyAMA0"},
		{Host{"linux", "amd64"}, "qemu-system-x86_64", "kvm", "ttyS0"},
		{Host{"darwin", "amd64"}, "qemu-system-x86_64", "hvf", "ttyS0"},
	}
	for _, c := range cases {
		vm := testVM()
		vm.Host = c.host
		cmd := build(t, vm)
		if cmd.Path != c.binary || !slices.Equal(opts(cmd.Args, "-accel"), []string{c.accel}) {
			t.Errorf("%v: %s -accel %q", c.host, cmd.Path, opts(cmd.Args, "-accel"))
		}
		if a := opts(cmd.Args, "-append"); len(a) != 1 || !strings.Contains(a[0], "console="+c.console) {
			t.Errorf("%v: -append %q", c.host, a)
		}
	}
	vm := testVM()
	vm.Host = Host{"windows", "amd64"}
	if _, err := Build(vm); err == nil {
		t.Error("built for windows")
	}
}

func TestDirectKernelBoot(t *testing.T) {
	cmd := build(t, testVM())
	if !slices.Equal(opts(cmd.Args, "-kernel"), []string{"/c/engine/base/vmlinuz"}) ||
		!slices.Equal(opts(cmd.Args, "-initrd"), []string{"/c/engine/base/initrd.img"}) {
		t.Errorf("kernel/initrd = %q %q", opts(cmd.Args, "-kernel"), opts(cmd.Args, "-initrd"))
	}
	if a := opts(cmd.Args, "-append")[0]; !strings.HasPrefix(a, "root=PARTUUID=0b6ff0a5-3d1e-4e57-a02c-4ad3c2a3b8a1 ro ") {
		t.Errorf("-append %q", a)
	}
	if len(opts(cmd.Args, "-bios"))+len(opts(cmd.Args, "-pflash")) > 0 {
		t.Errorf("firmware in %q", cmd.Args)
	}
	vm := testVM()
	vm.Base.Root = "PARTUUID=x init=/bin/sh"
	if _, err := Build(vm); err == nil {
		t.Error("built with an unchecked root")
	}
}

func TestTLSThroughFwCfg(t *testing.T) {
	vm := testVM()
	cmd := build(t, vm)
	want := []string{
		"name=opt/localllm/ca,file=/s/ca.pem",
		"name=opt/localllm/cert,file=/s/server.pem",
		"name=opt/localllm/key,file=/s/server-key.pem",
	}
	if got := opts(cmd.Args, "-fw_cfg"); !slices.Equal(got, want) {
		t.Errorf("-fw_cfg = %q, want %q", got, want)
	}
}

func TestCommaInPathEscaped(t *testing.T) {
	vm := testVM()
	vm.Base.Disk = "/c/a,b/disk.qcow2"
	vm.Certs.CA = "/s,x/ca.pem"
	cmd := build(t, vm)
	if d := opts(cmd.Args, "-drive")[0]; !strings.HasSuffix(d, "file=/c/a,,b/disk.qcow2") {
		t.Errorf("-drive %q", d)
	}
	if f := opts(cmd.Args, "-fw_cfg")[0]; !strings.HasSuffix(f, "file=/s,,x/ca.pem") {
		t.Errorf("-fw_cfg %q", f)
	}
}

package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// Layout is where localllm keeps engine data, outside every project and
// sandbox.
type Layout struct {
	Root string
}

// BaseDir holds the Engine base disk, its kernel and initrd, and BaseFile.
func (l Layout) BaseDir() string { return filepath.Join(l.Root, "base") }

// DownloadDir keeps the verified cloud image the base disk is built from.
func (l Layout) DownloadDir() string { return filepath.Join(l.Root, "downloads") }

// ProjectDir holds one project's engine data.
func (l Layout) ProjectDir(project string) string {
	return filepath.Join(l.Root, "projects", Name(project))
}

// ArchiveDir holds a project's image archives and their RecordFile.
func (l Layout) ArchiveDir(project string) string {
	return filepath.Join(l.ProjectDir(project), "images")
}

// Pidfile is where the project's running Engine VM's QEMU writes its PID.
func (l Layout) Pidfile(project string) string {
	return filepath.Join(l.ProjectDir(project), "qemu.pid")
}

const (
	// BaseFile describes a built Engine base disk.
	BaseFile = "base.json"
	// BaseDisk, BaseKernel and BaseInitrd are the files in BaseDir.
	BaseDisk   = "disk.qcow2"
	BaseKernel = "vmlinuz"
	BaseInitrd = "initrd.img"
	// BaseDiskSize is the base disk's virtual size.
	BaseDiskSize = "8G"
)

// BaseInfo describes a built Engine base disk.
type BaseInfo struct {
	// Definition is BaseDefinition for the architecture at build time.
	Definition string `json:"definition"`
	Arch       string `json:"arch"`
	// Root is the kernel's root= value, read from the built disk.
	Root string `json:"root"`
}

// rootArg is the root= values a built disk may report.
var rootArg = regexp.MustCompile(`^(PARTUUID|UUID)=[0-9A-Fa-f-]{8,36}$`)

// CheckRoot validates a root= value reported by the base disk build.
func CheckRoot(root string) error {
	if !rootArg.MatchString(root) {
		return fmt.Errorf("unexpected root %q", root)
	}
	return nil
}

// ErrNoBase is returned by LoadBase when no Engine base disk is built.
var ErrNoBase = errors.New("the Engine base disk isn't built; run 'localllm build'")

// LoadBase returns the built Engine base disk for arch, and a warning when
// it was built from an older definition.
func (l Layout) LoadBase(arch string) (Base, string, error) {
	dir := l.BaseDir()
	data, err := os.ReadFile(filepath.Join(dir, BaseFile))
	if errors.Is(err, fs.ErrNotExist) {
		return Base{}, "", ErrNoBase
	} else if err != nil {
		return Base{}, "", err
	}
	var info BaseInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return Base{}, "", fmt.Errorf("read %s: %w", filepath.Join(dir, BaseFile), err)
	}
	if info.Arch != arch {
		return Base{}, "", fmt.Errorf("the Engine base disk is for %s, not %s; run 'localllm build'", info.Arch, arch)
	}
	if err := CheckRoot(info.Root); err != nil {
		return Base{}, "", fmt.Errorf("the Engine base disk: %w; run 'localllm build'", err)
	}
	b := Base{
		Disk:   filepath.Join(dir, BaseDisk),
		Kernel: filepath.Join(dir, BaseKernel),
		Initrd: filepath.Join(dir, BaseInitrd),
		Root:   info.Root,
	}
	for _, f := range []string{b.Disk, b.Kernel, b.Initrd} {
		if _, err := os.Stat(f); err != nil {
			return Base{}, "", fmt.Errorf("the Engine base disk is incomplete (%v); run 'localllm build'", err)
		}
	}
	warning := ""
	if def, err := BaseDefinition(arch); err == nil && def != info.Definition {
		warning = "the Engine base disk was built from an older definition; run 'localllm build' to rebuild it"
	}
	return b, warning, nil
}

// CloudImage is the digest-checked Debian cloud image the Engine base disk
// is built from.
type CloudImage struct {
	URL    string
	SHA512 string
}

// cloudImages are Debian 13 genericcloud images, by GOARCH. To bump: take a
// newer dated release and its SHA512SUMS from cloud.debian.org.
var cloudImages = map[string]CloudImage{
	"arm64": {
		URL:    "https://cloud.debian.org/images/cloud/trixie/20261001-2618/debian-13-genericcloud-arm64-20261001-2618.qcow2",
		SHA512: "d8470b8c6c38fead046c794b5800a5a7b96672d5bcf543cc230ceb0c4b8ace05ed341a0c8928045422243fde26b2f1f2f58e99244c65709ffda2e3d4b674dd5a",
	},
	"amd64": {
		URL:    "https://cloud.debian.org/images/cloud/trixie/20261001-2618/debian-13-genericcloud-amd64-20261001-2618.qcow2",
		SHA512: "f46f0671a6e5bdec5291ab8972bae2f10e5408c2f64a74078f11efc2f06a436a9d0313ed50e0472542eeabf780e9f7c792ac0a314c6c20507fcd9fd81b468c3d",
	},
}

// CloudImageFor returns the cloud image for a GOARCH.
func CloudImageFor(arch string) (CloudImage, error) {
	img, ok := cloudImages[arch]
	if !ok {
		return CloudImage{}, fmt.Errorf("the Engine VM doesn't support %s hosts", arch)
	}
	return img, nil
}

// BaseDefinition identifies what a base disk for arch is built from, so a
// disk built by an older localllm can be told apart.
func BaseDefinition(arch string) (string, error) {
	img, err := CloudImageFor(arch)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(img.SHA512 + "\n" + userDataTemplate + "\n" + MetaData))
	return hex.EncodeToString(sum[:])[:16], nil
}

// MetaData is the NoCloud meta-data of the base disk build.
const MetaData = "instance-id: localllm-engine-base\nlocal-hostname: engine\n"

// UserData returns the cloud-config of the one online boot that builds the
// Engine base disk. seed is the build's HTTP endpoint on the host, ending
// in "/": the boot sends the kernel, initrd and root= value there, then
// "done", and powers off.
//
// The disk gets Docker, a published port range that is all the Engine VM
// forwards, and dockerd on TCP with --tlsverify. The TLS files come from
// fw_cfg at every boot (engine-tls.service), so the disk holds no keys.
// cloud-init is disabled afterwards, so a session's boot doesn't look for
// a seed, and nothing waits for a network that has no route out.
func UserData(seed string) string {
	return fmt.Sprintf(userDataTemplate, strconv.Quote(seed))
}

const userDataTemplate = `#cloud-config
package_update: true
packages: [docker.io, curl]
write_files:
  - path: /etc/sysctl.d/90-engine-ports.conf
    content: "net.ipv4.ip_local_port_range = 40000 40099\n"
  - path: /usr/local/sbin/engine-tls
    permissions: "0755"
    defer: true
    content: |
      #!/bin/sh
      set -eu
      modprobe qemu_fw_cfg
      mkdir -p /run/engine-tls
      chmod 700 /run/engine-tls
      for f in ca cert key; do
        cp "/sys/firmware/qemu_fw_cfg/by_name/opt/localllm/$f/raw" "/run/engine-tls/$f.pem"
      done
  - path: /etc/systemd/system/engine-tls.service
    defer: true
    content: |
      [Unit]
      Description=Engine API TLS material from fw_cfg
      Before=docker.service
      [Service]
      Type=oneshot
      RemainAfterExit=yes
      ExecStart=/usr/local/sbin/engine-tls
      [Install]
      WantedBy=multi-user.target
  - path: /etc/systemd/system/docker.service.d/tcp.conf
    defer: true
    content: |
      [Unit]
      Requires=engine-tls.service
      After=engine-tls.service
      [Service]
      ExecStart=
      ExecStart=/usr/sbin/dockerd -H fd:// -H tcp://0.0.0.0:2376 --tlsverify --tlscacert=/run/engine-tls/ca.pem --tlscert=/run/engine-tls/cert.pem --tlskey=/run/engine-tls/key.pem --containerd=/run/containerd/containerd.sock
runcmd:
  - - sh
    - -c
    - |
      set -eu
      seed=%s
      dpkg -s docker.io >/dev/null
      systemctl daemon-reload
      systemctl enable engine-tls.service docker.service
      systemctl mask systemd-networkd-wait-online.service
      touch /etc/cloud/cloud-init.disabled
      apt-get clean
      k=$(uname -r)
      root=$(tr ' ' '\n' < /proc/cmdline | sed -n 's/^root=//p')
      curl -fsS --data-binary "@/boot/vmlinuz-$k" "${seed}kernel"
      curl -fsS --data-binary "@/boot/initrd.img-$k" "${seed}initrd"
      curl -fsS --data-binary "$root" "${seed}root"
      curl -fsS -X POST "${seed}done"
power_state:
  mode: poweroff
  delay: now
`

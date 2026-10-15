package enginevm

import (
	"context"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"localllm/internal/engine"
	"localllm/internal/interrupt"
)

// buildTimeout bounds the online build boot, which installs Docker.
const buildTimeout = 20 * time.Minute

// BuildBase builds the Engine base disk for host into layout, unless one
// from the current definition is there. It downloads the Debian cloud
// image and checks it against its SHA-512, then runs one online boot that
// installs and configures Docker from a cloud-init seed served on the
// host's loopback, sends back the kernel, initrd and root= value for the
// direct kernel boot, and powers off. No project code is involved, and the
// result replaces the old disk only when complete.
func BuildBase(layout engine.Layout, host engine.Host, out io.Writer) error {
	m, err := engine.MachineFor(host)
	if err != nil {
		return err
	}
	qemu, err := FindQEMU(m)
	if err != nil {
		return err
	}
	qemuImg, err := exec.LookPath("qemu-img")
	if err != nil {
		return fmt.Errorf("%w (qemu-img not found)", ErrNoQEMU)
	}
	def, err := engine.BaseDefinition(host.Arch)
	if err != nil {
		return err
	}
	if _, warning, err := layout.LoadBase(host.Arch); err == nil && warning == "" {
		fmt.Fprintf(out, "the Engine base disk is up to date (%s)\n", layout.BaseDir())
		return nil
	}
	img, _ := engine.CloudImageFor(host.Arch)
	src, err := download(img, layout.DownloadDir(), out)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(layout.Root, 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(layout.Root, "base-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	disk := filepath.Join(tmp, engine.BaseDisk)
	for _, args := range [][]string{
		{"convert", "-O", "qcow2", src, disk},
		{"resize", "-q", disk, engine.BaseDiskSize},
	} {
		if b, err := exec.CommandContext(interrupt.Context(), qemuImg, args...).CombinedOutput(); err != nil {
			return fmt.Errorf("qemu-img %s: %w: %s", args[0], err, strings.TrimSpace(string(b)))
		}
	}

	seed, err := newSeedServer(tmp)
	if err != nil {
		return err
	}
	defer seed.Close()
	args := []string{
		"-name", "localllm-engine-build",
		"-nodefaults", "-no-user-config",
		"-machine", m.Machine,
		"-accel", m.Accel,
		"-cpu", "host",
		"-smp", strconv.Itoa(engine.DefaultCPUs),
		"-m", strconv.Itoa(engine.DefaultMemoryMiB),
	}
	if host.Arch == "arm64" {
		fw, err := firmware(qemu)
		if err != nil {
			return err
		}
		args = append(args, fw...)
	}
	serial := filepath.Join(layout.Root, "base-build-serial.log")
	args = append(args,
		"-drive", "if=virtio,format=qcow2,file="+strings.ReplaceAll(disk, ",", ",,"),
		// The one boot with a route out: it installs packages.
		"-netdev", "user,id=net0",
		"-device", "virtio-net-pci,netdev=net0",
		// cloud-init's NoCloud seed from the host, through QEMU's host alias.
		"-smbios", "type=1,serial=ds=nocloud;s="+seed.guestURL,
		"-display", "none",
		"-serial", "file:"+serial,
		"-monitor", "none",
	)
	fmt.Fprintf(out, "building the Engine base disk: one online boot installs Docker (about 2 minutes; console: %s)\n", serial)
	start := time.Now()
	if err := runBuildBoot(qemu, args); err != nil {
		if tail := tailFile(serial, 15); tail != "" {
			err = fmt.Errorf("%w\nlast lines of the console:\n%s", err, tail)
		}
		return err
	}
	root, err := seed.result()
	if err != nil {
		return fmt.Errorf("Engine base disk build: %w (console: %s)", err, serial)
	}
	info, err := json.Marshal(engine.BaseInfo{Definition: def, Arch: host.Arch, Root: root})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, engine.BaseFile), info, 0o600); err != nil {
		return err
	}
	if err := replaceDir(tmp, layout.BaseDir()); err != nil {
		return err
	}
	fmt.Fprintf(out, "built the Engine base disk in %s (%s)\n", time.Since(start).Round(time.Second), layout.BaseDir())
	return nil
}

// runBuildBoot runs QEMU until the guest powers off. An interrupt or the
// timeout kills it.
func runBuildBoot(qemu string, args []string) error {
	ctx, cancel := context.WithTimeout(interrupt.Context(), buildTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, qemu, args...)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return c.Process.Kill() }
	b, err := c.CombinedOutput()
	if err := interrupt.Err(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return fmt.Errorf("the Engine base disk build boot didn't finish within %s", buildTimeout)
	}
	if err != nil {
		return fmt.Errorf("the Engine base disk build boot failed: %w: %s", err, strings.TrimSpace(string(b)))
	}
	return nil
}

// firmware returns the QEMU options for UEFI firmware, which the arm64
// cloud image needs to boot through its own boot loader. Only the build
// boot uses it; sessions boot the kernel directly.
func firmware(qemu string) ([]string, error) {
	share := filepath.Join(filepath.Dir(filepath.Dir(qemu)), "share", "qemu")
	for _, f := range []string{
		filepath.Join(share, "edk2-aarch64-code.fd"),
		"/usr/share/qemu/edk2-aarch64-code.fd",
		"/usr/share/qemu-efi-aarch64/QEMU_EFI.fd",
		"/usr/share/AAVMF/AAVMF_CODE.fd",
	} {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		// The virt machine's flash is 64 MiB; smaller images are loaded as
		// a BIOS ROM instead.
		if info.Size() == 64<<20 {
			return []string{"-drive", "if=pflash,format=raw,readonly=on,file=" + strings.ReplaceAll(f, ",", ",,")}, nil
		}
		return []string{"-bios", f}, nil
	}
	return nil, errors.New("no UEFI firmware for arm64 found (edk2-aarch64-code.fd); it comes with QEMU")
}

// download fetches img into dir unless a copy with the right digest is
// there, and returns its path. The SHA-512 is checked before the file is
// used.
func download(img engine.CloudImage, dir string, out io.Writer) (string, error) {
	file := filepath.Join(dir, img.SHA512[:32]+".qcow2")
	if sum, err := sha512File(file); err == nil && sum == img.SHA512 {
		return file, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	fmt.Fprintf(out, "downloading %s\n", img.URL)
	req, err := http.NewRequestWithContext(interrupt.Context(), http.MethodGet, img.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download the Debian cloud image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", img.URL, resp.Status)
	}
	part := file + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer os.Remove(part)
	h := sha512.New()
	_, err = io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("download %s: %w", img.URL, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != img.SHA512 {
		return "", fmt.Errorf("download %s: SHA-512 %s doesn't match the pinned %s", img.URL, got, img.SHA512)
	}
	return file, os.Rename(part, file)
}

func sha512File(file string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha512.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// replaceDir moves the complete dir src into place at dst. A session using
// the old disk keeps its open files.
func replaceDir(src, dst string) error {
	old := dst + ".old"
	os.RemoveAll(old)
	if err := os.Rename(dst, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		os.Rename(old, dst)
		return err
	}
	return os.RemoveAll(old)
}

// seedServer serves the build boot's NoCloud seed and receives what the
// boot sends back. It listens on the host's loopback, which the build boot
// reaches as 10.0.2.2, under a random path.
type seedServer struct {
	srv      *http.Server
	dir      string
	guestURL string

	mu   sync.Mutex
	got  map[string]bool
	root string
}

// maxUpload bounds the kernel and initrd the build boot sends.
const maxUpload = 512 << 20

func newSeedServer(dir string) (*seedServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	token := make([]byte, 16)
	rand.Read(token)
	prefix := "/" + hex.EncodeToString(token) + "/"
	s := &seedServer{
		dir:      dir,
		guestURL: fmt.Sprintf("http://10.0.2.2:%d%s", ln.Addr().(*net.TCPAddr).Port, prefix),
		got:      map[string]bool{},
	}
	userData := engine.UserData(s.guestURL)
	mux := http.NewServeMux()
	mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, prefix)
		switch {
		case r.Method == http.MethodGet && name == "user-data":
			io.WriteString(w, userData)
		case r.Method == http.MethodGet && name == "meta-data":
			io.WriteString(w, engine.MetaData)
		case r.Method == http.MethodGet && name == "vendor-data":
		case r.Method == http.MethodPost && (name == "kernel" || name == "initrd"):
			file := map[string]string{"kernel": engine.BaseKernel, "initrd": engine.BaseInitrd}[name]
			if err := saveUpload(filepath.Join(dir, file), r.Body); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			s.mark(name, "")
		case r.Method == http.MethodPost && name == "root":
			b, _ := io.ReadAll(io.LimitReader(r.Body, 256))
			s.mark(name, strings.TrimSpace(string(b)))
		case r.Method == http.MethodPost && name == "done":
			s.mark(name, "")
		default:
			http.NotFound(w, r)
		}
	})
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go s.srv.Serve(ln)
	return s, nil
}

func (s *seedServer) mark(name, root string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got[name] = true
	if name == "root" {
		s.root = root
	}
}

func saveUpload(file string, body io.Reader) error {
	f, err := os.OpenFile(file, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(body, maxUpload+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && (n == 0 || n > maxUpload) {
		err = fmt.Errorf("upload of %d bytes", n)
	}
	return err
}

// result checks the build boot sent everything and returns its root=.
func (s *seedServer) result() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range []string{"kernel", "initrd", "root", "done"} {
		if !s.got[name] {
			return "", fmt.Errorf("the build boot powered off without sending its %s", name)
		}
	}
	if err := engine.CheckRoot(s.root); err != nil {
		return "", err
	}
	return s.root, nil
}

func (s *seedServer) Close() { s.srv.Close() }

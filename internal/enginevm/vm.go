// Package enginevm is a thin adapter over QEMU for the Engine VM: it starts
// a VM from a command line built by package engine, waits for its Docker
// API, loads image archives through that API, and stops it. It also builds
// the Engine base disk.
package enginevm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"localllm/internal/engine"
	"localllm/internal/interrupt"
)

// ErrNoQEMU is returned when the QEMU binary for the host isn't installed.
var ErrNoQEMU = errors.New("QEMU isn't installed")

// FindQEMU returns the path of the QEMU system emulator for m.
func FindQEMU(m engine.Machine) (string, error) {
	p, err := exec.LookPath(m.Binary)
	if err != nil {
		return "", fmt.Errorf("%w (%s not found; on macOS: brew install qemu)", ErrNoQEMU, m.Binary)
	}
	return p, nil
}

// FreePorts returns n distinct free TCP ports on 127.0.0.1. Another process
// could take one before QEMU binds it; QEMU then fails to start.
func FreePorts(n int) ([]int, error) {
	var lns []net.Listener
	defer func() {
		for _, ln := range lns {
			ln.Close()
		}
	}()
	var ports []int
	for range n {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("find free ports: %w", err)
		}
		lns = append(lns, ln)
		ports = append(ports, ln.Addr().(*net.TCPAddr).Port)
	}
	return ports, nil
}

// VM is a started Engine VM.
type VM struct {
	cmd     *exec.Cmd
	done    chan struct{}
	waitErr error
	pidfile string
	serial  string
	stderr  *bytes.Buffer
	api     string
	client  *http.Client
}

// Start runs cmd, built by engine.Build for vm, and returns without
// waiting for the VM to boot. clientCert and clientKey are the session's
// client certificate, and ca the CA its server certificate is signed by.
// QEMU gets its own process group, so a Ctrl-C at the terminal reaches
// localllm, which stops the VM, rather than QEMU directly.
func Start(cmd engine.Command, vm engine.VM, ca, clientCert, clientKey []byte) (*VM, error) {
	pair, err := tls.X509KeyPair(clientCert, clientKey)
	if err != nil {
		return nil, fmt.Errorf("engine client certificate: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("engine CA: no certificate")
	}
	if err := os.MkdirAll(filepath.Dir(vm.Pidfile), 0o700); err != nil {
		return nil, err
	}
	c := exec.Command(cmd.Path, cmd.Args...)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	v := &VM{
		cmd:     c,
		done:    make(chan struct{}),
		pidfile: vm.Pidfile,
		serial:  vm.Serial,
		stderr:  &bytes.Buffer{},
		api:     "https://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(vm.Ports.API)),
		client: &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12},
		}},
	}
	c.Stdout, c.Stderr = v.stderr, v.stderr
	if err := c.Start(); err != nil {
		return nil, fmt.Errorf("start the Engine VM: %w", err)
	}
	go func() {
		v.waitErr = c.Wait()
		close(v.done)
	}()
	return v, nil
}

// Done is closed when QEMU exits, which frees its host ports.
func (v *VM) Done() <-chan struct{} { return v.done }

// PID returns QEMU's process ID.
func (v *VM) PID() int { return v.cmd.Process.Pid }

// exited reports whether QEMU has exited.
func (v *VM) exited() bool {
	select {
	case <-v.done:
		return true
	default:
		return false
	}
}

// failure describes why the VM is unusable, from QEMU's output and the end
// of the console log.
func (v *VM) failure(what string) error {
	msg := what
	// QEMU's output is only complete, and safe to read, once it exited.
	if v.exited() {
		if out := strings.TrimSpace(v.stderr.String()); out != "" {
			msg += ": " + out
		}
	}
	if tail := tailFile(v.serial, 15); tail != "" {
		msg += "\nlast lines of the VM console (" + v.serial + "):\n" + tail
	}
	return errors.New(msg)
}

// WaitReady waits until the Docker API answers over TLS, until timeout, or
// until an interrupt.
func (v *VM) WaitReady(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(interrupt.Context(), timeout)
	defer cancel()
	for {
		if v.exited() {
			return v.failure("the Engine VM stopped while booting")
		}
		if err := v.ping(ctx); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			if err := interrupt.Err(); err != nil {
				return err
			}
			return v.failure(fmt.Sprintf("the Engine VM's Docker API didn't answer within %s", timeout))
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (v *VM) ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.api+"/_ping", nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ping: %s", resp.Status)
	}
	return nil
}

// Load loads each archive in dir into the VM's Docker and tags it with the
// reference it was declared as.
func (v *VM) Load(dir string, archives []engine.Archive) error {
	for _, a := range archives {
		if err := v.LoadTar(filepath.Join(dir, a.File), a.Ref); err != nil {
			return err
		}
	}
	return nil
}

// LoadTar loads one image archive into the VM's Docker and tags it ref.
func (v *VM) LoadTar(file, ref string) error {
	id, err := v.load(file)
	if err != nil {
		return fmt.Errorf("load engine image %s: %w", ref, err)
	}
	repo, tag := engine.SplitRef(ref)
	q := url.Values{"repo": {repo}, "tag": {tag}}
	if err := v.post("/images/"+url.PathEscape(id)+"/tag?"+q.Encode(), "", nil, nil); err != nil {
		return fmt.Errorf("tag engine image %s: %w", ref, err)
	}
	return nil
}

// load posts one archive to /images/load and returns the loaded image's
// ID. The archive was pulled by digest, so it has no tag.
func (v *VM) load(file string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	// Never follow a symlink to a host file and send it into the VM.
	if info, err := os.Lstat(file); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", file)
	}
	var id string
	err = v.post("/images/load?quiet=1", "application/x-tar", f, func(body io.Reader) error {
		dec := json.NewDecoder(body)
		for {
			var msg struct {
				Stream      string `json:"stream"`
				ErrorDetail *struct {
					Message string `json:"message"`
				} `json:"errorDetail"`
			}
			if err := dec.Decode(&msg); errors.Is(err, io.EOF) {
				return nil
			} else if err != nil {
				return err
			}
			if msg.ErrorDetail != nil {
				return errors.New(msg.ErrorDetail.Message)
			}
			for _, prefix := range []string{"Loaded image ID: ", "Loaded image: "} {
				if rest, ok := strings.CutPrefix(strings.TrimSpace(msg.Stream), prefix); ok {
					id = rest
				}
			}
		}
	})
	if err == nil && id == "" {
		err = errors.New("the engine reported no loaded image")
	}
	return id, err
}

// post sends a POST to the Docker API and passes a successful response's
// body to read, if set.
func (v *VM) post(path, contentType string, body io.Reader, read func(io.Reader) error) error {
	req, err := http.NewRequestWithContext(interrupt.Context(), http.MethodPost, v.api+path, body)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	if read != nil {
		return read(resp.Body)
	}
	return nil
}

// Stop stops the VM, discarding its throwaway disk layer. It runs even
// after an interrupt.
func (v *VM) Stop() {
	if !v.exited() {
		v.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-v.done:
		case <-time.After(5 * time.Second):
			v.cmd.Process.Kill()
			<-v.done
		}
	}
	v.client.CloseIdleConnections()
	os.Remove(v.pidfile)
}

// Recover stops a VM left running by a localllm that died, found through
// its pidfile. The process is only signalled if its command line names the
// pidfile, so a reused PID is never hit.
func Recover(pidfile string) error {
	data, err := os.ReadFile(pidfile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return os.Remove(pidfile)
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err == nil && strings.Contains(string(out), pidfile) {
		p, _ := os.FindProcess(pid)
		p.Signal(syscall.SIGTERM)
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			if p.Signal(syscall.Signal(0)) != nil {
				break
			}
		}
		if p.Signal(syscall.Signal(0)) == nil {
			if err := p.Kill(); err != nil {
				return fmt.Errorf("stop the orphaned Engine VM (PID %d): %w", pid, err)
			}
		}
	}
	if err := os.Remove(pidfile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// tailFile returns the last n lines of file, or "".
func tailFile(file string, n int) string {
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	return strings.Join(lines, "\n")
}

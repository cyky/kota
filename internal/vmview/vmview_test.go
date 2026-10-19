package vmview

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func project(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".envrc"), []byte("use flake\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// local runs Script on the host in dir, as the container would in the VM:
// the host's view stands in for the VM's. On macOS, a stat on PATH turns
// GNU stat's arguments into BSD stat's.
func local(t *testing.T, dir string) Start {
	t.Helper()
	env := os.Environ()
	if runtime.GOOS == "darwin" {
		bin := t.TempDir()
		shim := "#!/bin/sh\n# stat -c FORMAT -- PATH\nexec /usr/bin/stat -f 'file|%z|%Fm' \"$4\"\n"
		if err := os.WriteFile(filepath.Join(bin, "stat"), []byte(shim), 0o755); err != nil {
			t.Fatal(err)
		}
		env = append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	return func(args []string) (io.Reader, func() error, error) {
		cmd := exec.Command("sh", append([]string{"-c", Script, "sh"}, args...)...)
		cmd.Dir, cmd.Env, cmd.Stderr = dir, env, os.Stderr
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, nil, err
		}
		return out, func() error {
			cmd.Process.Signal(os.Interrupt)
			cmd.Process.Kill()
			cmd.Wait()
			return nil
		}, nil
	}
}

func TestWaitMatches(t *testing.T) {
	dir := project(t)
	if err := Wait(dir, []string{".envrc", ".git", "missing", "-dash"}, 5*time.Second, local(t, dir)); err != nil {
		t.Fatal(err)
	}
}

// The host's states as Wait passes them, one per path.
func TestWaitArgs(t *testing.T) {
	dir := project(t)
	info, err := os.Lstat(filepath.Join(dir, ".envrc"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	start := func(args []string) (io.Reader, func() error, error) {
		got = args
		return strings.NewReader("ok\n"), func() error { return nil }, nil
	}
	if err := Wait(dir, []string{".envrc", ".git", "missing"}, time.Second, start); err != nil {
		t.Fatal(err)
	}
	mtime := info.ModTime()
	want := []string{"0.25",
		"./.envrc", fmt.Sprintf("file|10|%d.%09d", mtime.Unix(), mtime.Nanosecond()),
		"./.git", "dir",
		"./missing", "missing"}
	if !slices.Equal(got, want) {
		t.Errorf("args = %q\nwant %q", got, want)
	}
}

// The script reports each path whose state differs from the host's it
// was given, and an mtime without nanoseconds compares to the second.
func TestScriptReportsDifferences(t *testing.T) {
	dir := project(t)
	out, stop, err := local(t, dir)([]string{"0.25",
		"./.envrc", "file|99|1.000000000",
		"./.git", "file|0|1.0",
		"./missing", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(line)
	if len(f) != 3 || f[0] != "differ" || !strings.HasPrefix(f[1], "0=file|10|") || f[2] != "1=dir" {
		t.Errorf("line = %q", line)
	}
}

// After its timeout, Wait stops the container and names each path that
// still differs, with both states.
func TestWaitRefusesAfterTimeout(t *testing.T) {
	dir := project(t)
	var stopped atomic.Bool
	start := func(args []string) (io.Reader, func() error, error) {
		r, w := io.Pipe()
		go func() {
			for !stopped.Load() {
				if _, err := io.WriteString(w, "differ 0=missing 2=file|3|1792395829.839168230\n"); err != nil {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
		return r, func() error { stopped.Store(true); w.Close(); return nil }, nil
	}
	began := time.Now()
	err := Wait(dir, []string{".envrc", ".git", "missing"}, 200*time.Millisecond, start)
	if err == nil {
		t.Fatal("Wait didn't refuse")
	}
	if d := time.Since(began); d < 200*time.Millisecond || d > 2*time.Second {
		t.Errorf("refused after %s, want the 200ms timeout", d)
	}
	if !stopped.Load() {
		t.Error("container not stopped")
	}
	msg := err.Error()
	for _, want := range []string{"after 200ms", "2 protected or masked path(s)",
		".envrc (host: a file of 10 bytes, modified ", "; Docker VM: missing)",
		"missing (host: missing; Docker VM: a file of 3 bytes, modified 2026-10-19T"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, ".git (") {
		t.Errorf("error names a path that matched:\n%s", msg)
	}
}

func TestWaitContainerFails(t *testing.T) {
	dir := project(t)
	start := func(args []string) (io.Reader, func() error, error) {
		return strings.NewReader(""), func() error { return nil }, nil
	}
	if err := Wait(dir, []string{".envrc"}, time.Second, start); err == nil || !strings.Contains(err.Error(), "without a result") {
		t.Errorf("err = %v", err)
	}
}

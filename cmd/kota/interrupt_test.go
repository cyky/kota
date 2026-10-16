package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestMain lets a test run the test binary as kota itself, so a real
// signal can be sent to it.
func TestMain(m *testing.M) {
	if os.Getenv("KOTA_TEST_MAIN") == "1" {
		os.Args = append([]string{"kota"}, strings.Fields(os.Getenv("KOTA_TEST_ARGS"))...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// syncBuffer is a bytes.Buffer safe to read while a process writes it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fakeDocker puts a docker script on PATH that logs each call to the
// returned file, then runs body (a case statement over "$*").
func fakeDocker(t *testing.T, body string) (log string) {
	t.Helper()
	bin := t.TempDir()
	log = filepath.Join(bin, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// startKota runs kota with args in the test's environment, stdin
// held open and never written.
func startKota(t *testing.T, args ...string) (*exec.Cmd, *syncBuffer) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "KOTA_TEST_MAIN=1", "KOTA_TEST_ARGS="+strings.Join(args, " "))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close() })
	stderr := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = stderr, stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	return cmd, stderr
}

func waitFor(t *testing.T, what string, cond func() bool, output *syncBuffer) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); !cond(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; output:\n%s", what, output)
		}
	}
}

// interruptAndWait sends SIGINT to kota alone, not its process group,
// so its own handling has to stop its children, and returns the exit code.
func interruptAndWait(t *testing.T, cmd *exec.Cmd, output *syncBuffer) int {
	t.Helper()
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("kota didn't exit after SIGINT; output:\n%s", output)
	}
	if !strings.Contains(output.String(), "kota: interrupted") {
		t.Errorf("output lacks the interrupted message:\n%s", output)
	}
	return cmd.ProcessState.ExitCode()
}

// assertNothingLeft checks the project and cache hold nothing from the
// session, and the project's lease is free with an empty record, so the
// next command starts without recovery.
func assertNothingLeft(t *testing.T, project string) {
	t.Helper()
	for _, rel := range []string{".git/commondir", ".envrc", ".kotaignore", "kota.yaml"} {
		if _, err := os.Lstat(filepath.Join(project, rel)); !os.IsNotExist(err) {
			t.Errorf("%s left in the project: %v", rel, err)
		}
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(cache, "kota", "sessions")); len(entries) > 0 {
		t.Errorf("session dirs left: %v", entries)
	}
	locks, _ := filepath.Glob(filepath.Join(cache, "kota", "registry", "*.lock"))
	for _, l := range locks {
		if info, err := os.Stat(l); err != nil || info.Size() != 0 {
			t.Errorf("lease record %s not cleared: %v", l, err)
		}
	}
	lease, err := lockProject(sessionArgs{dir: project}, "run")
	if err != nil {
		t.Fatalf("project still locked: %v", err)
	}
	releaseProject(lease)
}

// Ctrl-C at prep's confirmation leaves nothing behind: the question comes
// before anything is written to the host or Docker is called.
func TestInterruptAtPrepPrompt(t *testing.T) {
	project := fakeHome(t, map[string]string{"package.json": "{}", "package-lock.json": "{}"})
	calls := fakeDocker(t, "")
	cmd, output := startKota(t, "prep", project)
	waitFor(t, "the prep prompt", func() bool { return strings.Contains(output.String(), "Type y to run prep") }, output)

	if code := interruptAndWait(t, cmd, output); code != 130 {
		t.Errorf("exit code = %d, want 130; output:\n%s", code, output)
	}
	if data, _ := os.ReadFile(calls); len(data) > 0 {
		t.Errorf("docker called before the answer:\n%s", data)
	}
	assertNothingLeft(t, project)
}

// An interrupt while the probes run stops them, never starts the agent,
// and tears down the containers, placeholders and session dir.
func TestInterruptDuringProbes(t *testing.T) {
	project := fakeHome(t, map[string]string{"README.md": "test"})
	t.Setenv("KOTA_GATEWAY_KEY", "")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
	probing := filepath.Join(t.TempDir(), "probing")
	calls := fakeDocker(t, `case "$*" in
  *wget*) printf '%s\n' '{"data":[{"id":"qwen3.8-27b"}]}' ;;
  *'run --rm -T agent'*) touch `+probing+`; exec sleep 30 ;;
esac`)
	cmd, output := startKota(t, "run", project)
	waitFor(t, "the probes", func() bool { _, err := os.Stat(probing); return err == nil }, output)

	if code := interruptAndWait(t, cmd, output); code != 130 {
		t.Errorf("exit code = %d, want 130; output:\n%s", code, output)
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	if !strings.Contains(log, " down --remove-orphans") {
		t.Errorf("containers not torn down; docker calls:\n%s", log)
	}
	if strings.Contains(log, "run --rm agent") {
		t.Errorf("agent started after the interrupt; docker calls:\n%s", log)
	}
	assertNothingLeft(t, project)
}

// An interrupt during the agent's command, without a terminal, tears down
// and still reports what the session changed.
func TestInterruptDuringCommand(t *testing.T) {
	project := fakeHome(t, map[string]string{"README.md": "test"})
	t.Setenv("KOTA_GATEWAY_KEY", "")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
	t.Setenv("TEST_PROJECT", project)
	calls := fakeDocker(t, `case "$*" in
  *wget*) printf '%s\n' '{"data":[{"id":"qwen3.8-27b"}]}' ;;
  *'run --rm agent'*) echo 'all:' > "$TEST_PROJECT/Makefile"; exec sleep 30 ;;
esac`)
	cmd, output := startKota(t, "run", "--unsafe-skip-check", project, "--", "make")
	waitFor(t, "the command", func() bool {
		_, err := os.Stat(filepath.Join(project, "Makefile"))
		return err == nil
	}, output)

	if code := interruptAndWait(t, cmd, output); code != 130 {
		t.Errorf("exit code = %d, want 130; output:\n%s", code, output)
	}
	if !strings.Contains(output.String(), "added     Makefile") {
		t.Errorf("change report missing:\n%s", output)
	}
	if data, _ := os.ReadFile(calls); !strings.Contains(string(data), " down --remove-orphans") {
		t.Errorf("containers not torn down; docker calls:\n%s", data)
	}
	assertNothingLeft(t, project)
}

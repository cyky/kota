package sessionregistry

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func acquire(t *testing.T, r *Registry, project, agent, model string) *Lease {
	t.Helper()
	lease, err := r.Acquire(project, agent, model)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lease.Close(); err != nil {
			t.Error(err)
		}
	})
	return lease
}

func TestAcquireBusyRelease(t *testing.T) {
	r := &Registry{Dir: t.TempDir()}
	lease := acquire(t, r, "/projects/a", "run (pi)", "model-a")
	// Another registry instance must see the same OS lock, even in this PID.
	other := &Registry{Dir: r.Dir}
	_, err := other.Acquire("/projects/a", "prep", "")
	var busy *Busy
	if !errors.As(err, &busy) {
		t.Fatalf("acquire = %v, want Busy", err)
	}
	if busy.Session.Agent != "run (pi)" || busy.Session.PID != os.Getpid() || !strings.Contains(err.Error(), "/projects/a") {
		t.Errorf("busy session = %+v (%v)", busy.Session, err)
	}
	active, err := other.Active()
	if err != nil || len(active) != 1 || active[0].Model != "model-a" {
		t.Fatalf("active = %+v, %v", active, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	other.Recover = func(string) error { t.Error("cleanly released lease needs no recovery"); return nil }
	acquire(t, other, "/projects/a", "prep", "")
	if _, err := lease.SetModel("x", "gw:1"); !errors.Is(err, os.ErrClosed) {
		t.Errorf("SetModel on closed lease = %v", err)
	}
}

func TestDifferentProjectsAndClashes(t *testing.T) {
	const gw = "host.docker.internal:8686"
	r := &Registry{Dir: t.TempDir()}
	a := acquire(t, r, "/projects/a", "run (opencode)", "model-a")
	b := acquire(t, r, "/projects/b", "run (pi)", "model-a")
	acquire(t, r, "/projects/shell", "shell (pi)", "")
	acquire(t, r, "/projects/prep", "prep", "")
	if _, err := a.SetModel("model-a", gw); err != nil {
		t.Fatal(err)
	}
	if clashes, err := b.SetModel("model-a", gw); err != nil || len(clashes) != 0 {
		t.Fatalf("same-model clashes = %v, %v", clashes, err)
	}
	clashes, err := b.SetModel("model-b", gw)
	if err != nil || len(clashes) != 1 || clashes[0].Project != "/projects/a" || clashes[0].Model != "model-a" {
		t.Fatalf("different-model clashes = %+v, %v", clashes, err)
	}
	if clashes, err := b.SetModel("model-b", "host.docker.internal:4000"); err != nil || len(clashes) != 0 {
		t.Fatalf("other-gateway clashes = %v, %v", clashes, err)
	}
	if clashes, err := a.SetModel("model-c", gw); err != nil || len(clashes) != 0 {
		t.Fatalf("clashes with a session on another gateway = %v, %v", clashes, err)
	}
	if _, err := b.SetModel("model-b", gw); err != nil {
		t.Fatal(err)
	}
	if clashes, err := a.SetModel("model-b", gw); err != nil || len(clashes) != 0 {
		t.Fatalf("updated-model clashes = %v, %v", clashes, err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if clashes, err := a.SetModel("model-c", gw); err != nil || len(clashes) != 0 {
		t.Fatalf("released-session clashes = %v, %v", clashes, err)
	}
}

// TestClashWithOlderRecord checks a record without a gateway, as an older
// kota writes, is compared as if on the same gateway.
func TestClashWithOlderRecord(t *testing.T) {
	r := &Registry{Dir: t.TempDir()}
	a := acquire(t, r, "/projects/a", "run (opencode)", "")
	b := acquire(t, r, "/projects/b", "run (pi)", "")
	if _, err := a.SetModel("model-a", ""); err != nil {
		t.Fatal(err)
	}
	clashes, err := b.SetModel("model-b", "host.docker.internal:8686")
	if err != nil || len(clashes) != 1 || clashes[0].Gateway != "" {
		t.Fatalf("clashes with an older record = %+v, %v", clashes, err)
	}
}

func TestConcurrentAcquire(t *testing.T) {
	r := &Registry{Dir: t.TempDir()}
	const count = 16
	start := make(chan struct{})
	results := make(chan *Lease, count)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			<-start
			lease, err := r.Acquire("/projects/a", "run", "")
			var busy *Busy
			if err != nil && !errors.As(err, &busy) {
				t.Errorf("acquire: %v", err)
			}
			results <- lease
		})
	}
	close(start)
	wg.Wait()
	close(results)
	var owners int
	for lease := range results {
		if lease != nil {
			owners++
			lease.Close()
		}
	}
	if owners != 1 {
		t.Fatalf("%d owners, want 1", owners)
	}
}

func TestSimultaneousModelPublication(t *testing.T) {
	r := &Registry{Dir: t.TempDir()}
	a := acquire(t, r, "/projects/a", "run", "")
	b := acquire(t, r, "/projects/b", "run", "")
	start := make(chan struct{})
	counts := make(chan int, 2)
	for i, lease := range []*Lease{a, b} {
		go func() {
			<-start
			clashes, err := lease.SetModel(fmt.Sprintf("model-%d", i), "gw:1")
			if err != nil {
				t.Error(err)
			}
			counts <- len(clashes)
		}()
	}
	close(start)
	if total := <-counts + <-counts; total != 1 {
		t.Fatalf("simultaneous starts reported %d clashes, want 1", total)
	}
}

// The helper is killed without closing its lease. This tests actual process
// death and OS lock release, rather than approximating it with a fake PID.
func TestCrashedProcessRecovery(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRegistryProcessHelper$")
	cmd.Env = append(os.Environ(), "KOTA_REGISTRY_HELPER="+dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("helper: %q, %v", line, err)
	}
	r := &Registry{Dir: dir}
	active, err := r.Active()
	if err != nil || len(active) != 1 || active[0].PID != cmd.Process.Pid {
		t.Fatalf("live helper = %+v, %v", active, err)
	}
	if _, err := r.Acquire("/projects/crashed", "run", ""); err == nil {
		t.Fatal("acquired helper's live lock")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	if active, err := r.Active(); err != nil || len(active) != 0 {
		t.Fatalf("crashed session still active = %+v, %v", active, err)
	}
	// Recovery failures must refuse startup and keep the stale record so a
	// later attempt can finish cleaning up the orphaned container.
	failed := errors.New("Docker unavailable")
	r.Recover = func(string) error { return failed }
	if _, err := r.Acquire("/projects/crashed", "run", ""); !errors.Is(err, failed) {
		t.Fatalf("recovery failure = %v", err)
	}
	var recovered int
	r.Recover = func(project string) error {
		if project != "/projects/crashed" {
			t.Errorf("recover project = %q", project)
		}
		recovered++
		return nil
	}
	lease := acquire(t, r, "/projects/crashed", "shell", "")
	if recovered != 1 || lease.session.PID != os.Getpid() {
		t.Fatalf("recovery count = %d, new owner = %+v", recovered, lease.session)
	}
}

func TestRegistryProcessHelper(t *testing.T) {
	dir := os.Getenv("KOTA_REGISTRY_HELPER")
	if dir == "" {
		return
	}
	r := &Registry{Dir: dir}
	lease, err := r.Acquire("/projects/crashed", "run (pi)", "old-model")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	fmt.Println("ready")
	var b [1]byte
	os.Stdin.Read(b[:])
}

func TestPartialRecordRecovery(t *testing.T) {
	r := &Registry{Dir: t.TempDir()}
	// A process may die halfway through writing its metadata. A free OS
	// lock is enough to recover, even when its record can't be decoded.
	if err := os.WriteFile(r.filename("/projects/a"), []byte(`{"Project":`), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	r.Recover = func(string) error { called = true; return nil }
	acquire(t, r, "/projects/a", "run", "")
	if !called {
		t.Fatal("partial record wasn't recovered")
	}
	if info, err := os.Stat(filepath.Join(r.Dir, ".guard")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("registry guard permissions: %v, %v", info, err)
	}
}

// A lock list left by a killed session is unlocked after its containers
// are recovered, before Acquire returns; one left by a session that ended
// is unlocked too. A failed unlock refuses the new session.
func TestLeftoverLockListUnlocked(t *testing.T) {
	r := &Registry{Dir: t.TempDir()}
	if err := os.WriteFile(r.filename("/projects/crashed"), []byte(`{"Project":"/projects/crashed","PID":123}`), 0o600); err != nil {
		t.Fatal(err)
	}
	list := r.LockList("/projects/crashed")
	if filepath.Dir(list) != r.Dir || list == r.filename("/projects/crashed") || list == r.LockList("/projects/other") {
		t.Fatalf("lock list %s", list)
	}
	if err := os.WriteFile(list, []byte(`"/projects/crashed"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls []string
	r.Recover = func(string) error { calls = append(calls, "recover"); return nil }
	r.Unlock = func(l string) error {
		if l != list {
			t.Errorf("unlock %s, want %s", l, list)
		}
		calls = append(calls, "unlock")
		return os.Remove(l)
	}
	lease := acquire(t, r, "/projects/crashed", "run", "")
	if strings.Join(calls, ",") != "recover,unlock" {
		t.Fatalf("calls = %v", calls)
	}
	lease.Close()

	// A list left after a clean release: no recovery, still unlocked.
	calls = nil
	if err := os.WriteFile(list, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	acquire(t, r, "/projects/crashed", "run", "").Close()
	if strings.Join(calls, ",") != "unlock" {
		t.Fatalf("calls = %v", calls)
	}

	failed := errors.New("operation not permitted")
	r.Unlock = func(string) error { return failed }
	if err := os.WriteFile(list, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Acquire("/projects/crashed", "run", ""); !errors.Is(err, failed) {
		t.Fatalf("unlock failure = %v", err)
	}
	// No list, no unlock.
	os.Remove(list)
	acquire(t, r, "/projects/crashed", "run", "")
}

func TestRecoveryDoesNotHoldRegistryGuard(t *testing.T) {
	r := &Registry{Dir: t.TempDir()}
	if err := os.WriteFile(r.filename("/projects/crashed"), []byte(`{"Project":"/projects/crashed","Agent":"run","PID":123}`), 0o600); err != nil {
		t.Fatal(err)
	}
	entered, resume := make(chan struct{}), make(chan struct{})
	r.Recover = func(string) error {
		close(entered)
		<-resume
		return nil
	}
	recovered := make(chan error, 1)
	go func() {
		lease, err := r.Acquire("/projects/crashed", "run", "")
		if err == nil {
			err = lease.Close()
		}
		recovered <- err
	}()
	<-entered
	operations := make(chan error, 1)
	go func() {
		_, err := r.Acquire("/projects/crashed", "shell", "")
		var busy *Busy
		if !errors.As(err, &busy) {
			operations <- fmt.Errorf("recovering project isn't locked: %v", err)
			return
		}
		lease, err := r.Acquire("/projects/other", "run", "")
		if err != nil {
			operations <- err
			return
		}
		_, modelErr := lease.SetModel("model-b", "gw:1")
		_, activeErr := r.Active()
		operations <- errors.Join(modelErr, activeErr, lease.Close())
	}()
	completed := false
	select {
	case err := <-operations:
		completed = true
		if err != nil {
			t.Error(err)
		}
	case <-time.After(2 * time.Second):
		t.Error("recovery blocked Busy reporting, unrelated Acquire, SetModel, Active or Close")
	}
	close(resume)
	if err := <-recovered; err != nil {
		t.Error(err)
	}
	if !completed {
		<-operations
	}
}

func TestActiveSkipsVanishedEntries(t *testing.T) {
	r := &Registry{Dir: t.TempDir()}
	acquire(t, r, "/projects/a", "run", "model-a")
	// A dangling symlink gives the same ENOENT at OpenFile as an entry
	// disappearing between ReadDir and OpenFile, without a timing race.
	if err := os.Symlink(filepath.Join(r.Dir, "gone"), filepath.Join(r.Dir, "vanished.lock")); err != nil {
		t.Fatal(err)
	}
	active, err := r.Active()
	if err != nil || len(active) != 1 || active[0].Project != "/projects/a" {
		t.Fatalf("vanished entry disrupted active sessions: %+v, %v", active, err)
	}
}

func TestExclusiveCacheCleanup(t *testing.T) {
	r := &Registry{Dir: t.TempDir()}
	first := acquire(t, r, "/projects/a", "prep", "")
	second := acquire(t, r, "/projects/b", "run", "model")
	// A lease without a container still protects the caches, including
	// confirmations and the gap between prep's fetch/install containers.
	_, err := r.AcquireExclusive("/projects/clean", "clean", "")
	var busy *CachesBusy
	if !errors.As(err, &busy) || !busy.Exclusive {
		t.Fatalf("cache reset while projects active = %v", err)
	}
	first.Close()
	if _, err := r.AcquireExclusive("/projects/clean", "clean", ""); err == nil {
		t.Fatal("cache reset ignored remaining active project")
	}
	second.Close()
	lease, err := r.AcquireExclusive("/projects/clean", "clean", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lease.Close() })
	if _, err := r.Acquire("/projects/new", "run", ""); !errors.As(err, &busy) || busy.Exclusive {
		t.Fatalf("session started during cache reset: %v", err)
	}
	if _, err := r.AcquireExclusive("/projects/other", "clean", ""); !errors.As(err, &busy) {
		t.Fatalf("second cache reset started: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	acquire(t, r, "/projects/new", "run", "")
}

func TestFailedAcquireReleasesCacheReservation(t *testing.T) {
	r := &Registry{Dir: t.TempDir(), Recover: func(string) error { return errors.New("recovery failed") }}
	if err := os.WriteFile(r.filename("/projects/a"), []byte(`{"Project":"/projects/a"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AcquireExclusive("/projects/a", "clean", ""); err == nil {
		t.Fatal("failed recovery accepted")
	}
	lease := acquire(t, r, "/projects/b", "run", "")
	lease.Close()
	other, err := r.AcquireExclusive("/projects/c", "clean", "")
	if err != nil {
		t.Fatalf("failure leaked cache reservation: %v", err)
	}
	other.Close()
}

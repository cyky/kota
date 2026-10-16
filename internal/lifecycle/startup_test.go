package lifecycle

import (
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder logs the calls of the fakes, in order.
type recorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recorder) add(c string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

type fakeSandbox struct {
	r      *recorder
	killed chan string
}

func (s *fakeSandbox) Close() { s.r.add("sandbox close") }
func (s *fakeSandbox) Kill(service string) error {
	s.r.add("kill " + service)
	s.killed <- service
	return nil
}

type fakeEngine struct {
	r    *recorder
	fail string
	done chan struct{}
}

func (e *fakeEngine) step(name string) error {
	e.r.add(name)
	if e.fail == name {
		return errors.New(name + " failed")
	}
	return nil
}
func (e *fakeEngine) Start() error          { return e.step("engine start") }
func (e *fakeEngine) Done() <-chan struct{} { return e.done }
func (e *fakeEngine) Ready(io.Writer) error { return e.step("engine ready") }
func (e *fakeEngine) Stop()                 { e.r.add("engine stop") }

// startup is a Startup over fakes in which the step named fail fails, and a
// signal arrives at the interrupt check numbered interruptAt (1-based; 0 for
// none).
func startup(fail string, interruptAt int, withEngine bool) (Startup[*fakeSandbox], *recorder, *fakeEngine, *fakeSandbox) {
	r := &recorder{}
	sb := &fakeSandbox{r: r, killed: make(chan string, 1)}
	e := &fakeEngine{r: r, fail: fail, done: make(chan struct{})}
	checks := 0
	st := Startup[*fakeSandbox]{
		EngineService: "engine",
		StartSandbox: func() (*fakeSandbox, error) {
			r.add("sandbox start")
			if fail == "sandbox start" {
				return nil, errors.New("sandbox start failed")
			}
			return sb, nil
		},
		Configure: func(*fakeSandbox) error { return e.step("configure") },
		Verify:    func(*fakeSandbox) error { return e.step("verify") },
		Out:       io.Discard,
		Interrupted: func() error {
			checks++
			if checks == interruptAt {
				r.add("signal")
				return errors.New("interrupted")
			}
			return nil
		},
	}
	if withEngine {
		st.Engine = e
	}
	return st, r, e, sb
}

func TestStartOrder(t *testing.T) {
	st, r, _, _ := startup("", 0, true)
	sb, teardown, err := st.Start()
	if err != nil || sb == nil {
		t.Fatalf("Start = %v, %v", sb, err)
	}
	want := []string{"engine start", "sandbox start", "configure", "engine ready", "verify"}
	if got := r.list(); !slices.Equal(got, want) {
		t.Errorf("start = %q, want %q", got, want)
	}
	teardown()
	want = append(want, "sandbox close", "engine stop")
	if got := r.list(); !slices.Equal(got, want) {
		t.Errorf("after teardown = %q, want %q", got, want)
	}
	teardown()
	if got := r.list(); len(got) != len(want) {
		t.Errorf("a second teardown stopped again: %q", got)
	}
}

func TestStartWithoutEngine(t *testing.T) {
	st, r, _, _ := startup("", 0, false)
	_, teardown, err := st.Start()
	if err != nil {
		t.Fatal(err)
	}
	teardown()
	want := []string{"sandbox start", "configure", "verify", "sandbox close"}
	if got := r.list(); !slices.Equal(got, want) {
		t.Errorf("calls = %q, want %q", got, want)
	}
}

// TestStartFailureStopsWhatStarted fails each step in turn: what started
// before it is stopped, last first, and nothing after it runs.
func TestStartFailureStopsWhatStarted(t *testing.T) {
	cases := map[string][]string{
		"engine start":  {"engine start"},
		"sandbox start": {"engine start", "sandbox start", "engine stop"},
		"configure":     {"engine start", "sandbox start", "configure", "sandbox close", "engine stop"},
		"engine ready":  {"engine start", "sandbox start", "configure", "engine ready", "sandbox close", "engine stop"},
		"verify":        {"engine start", "sandbox start", "configure", "engine ready", "verify", "sandbox close", "engine stop"},
	}
	for fail, want := range cases {
		t.Run(fail, func(t *testing.T) {
			st, r, _, _ := startup(fail, 0, true)
			sb, teardown, err := st.Start()
			if err == nil || !strings.Contains(err.Error(), fail) || sb != nil || teardown != nil {
				t.Fatalf("Start = %v, %v, teardown %v; want the %s error", sb, err, teardown != nil, fail)
			}
			if got := r.list(); !slices.Equal(got, want) {
				t.Errorf("calls = %q, want %q", got, want)
			}
		})
	}
}

// TestStartInterrupted delivers a signal at each check between steps.
func TestStartInterrupted(t *testing.T) {
	cases := map[int][]string{
		1: {"engine start", "sandbox start", "signal", "sandbox close", "engine stop"},
		2: {"engine start", "sandbox start", "configure", "engine ready", "signal", "sandbox close", "engine stop"},
		3: {"engine start", "sandbox start", "configure", "engine ready", "verify", "signal", "sandbox close", "engine stop"},
	}
	for at, want := range cases {
		st, r, _, _ := startup("", at, true)
		if _, _, err := st.Start(); err == nil {
			t.Errorf("check %d: started despite the signal", at)
		}
		if got := r.list(); !slices.Equal(got, want) {
			t.Errorf("check %d: calls = %q, want %q", at, got, want)
		}
	}
}

func TestEngineExitKillsForwarder(t *testing.T) {
	st, _, e, sb := startup("", 0, true)
	_, teardown, err := st.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer teardown()
	close(e.done)
	select {
	case service := <-sb.killed:
		if service != "engine" {
			t.Errorf("killed %q", service)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the engine forwarder wasn't killed after the VM exited")
	}
}

func TestEngineExitAfterTeardownKillsNothing(t *testing.T) {
	st, _, e, sb := startup("", 0, true)
	_, teardown, err := st.Start()
	if err != nil {
		t.Fatal(err)
	}
	teardown()
	close(e.done)
	select {
	case service := <-sb.killed:
		t.Errorf("killed %q after teardown", service)
	case <-time.After(100 * time.Millisecond):
	}
}

package interrupt

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

// The context is process-wide and can't be reset, so one test walks
// through before and after the signal.
func TestSignalCancels(t *testing.T) {
	if err := Err(); err != nil {
		t.Fatalf("Err before a signal = %v", err)
	}
	if !Ask(func() bool { return true }) || Ask(func() bool { return false }) {
		t.Fatal("Ask doesn't return the prompt's answer")
	}

	Catch()
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case <-Context().Done():
	case <-time.After(5 * time.Second):
		t.Fatal("SIGINT didn't cancel the context")
	}
	if !errors.Is(Err(), ErrInterrupted) {
		t.Errorf("Err after a signal = %v", Err())
	}
	// A prompt still waiting for input gives up; a second signal is ignored
	// instead of killing the process.
	if Ask(func() bool { select {} }) {
		t.Error("Ask said yes after a signal")
	}
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
}

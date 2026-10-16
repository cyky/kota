// Package lifecycle holds the order a Session starts and stops in, shared by
// run, shell and check:
//
//  1. The Engine VM, if the session has one, starts first, so it boots
//     while the Sandbox starts.
//  2. The Sandbox starts. From here on, if the Engine VM exits (the agent
//     can power it off as root), the engine forwarder is killed at once, so
//     the agent can't reach whatever binds the freed host ports next.
//  3. Configure runs once the Sandbox can reach the gateway.
//  4. The Engine VM must be ready, with its images loaded.
//  5. Verify runs the probes.
//
// On any failure, or a signal between steps, everything already started is
// stopped again, last first: the watch is disarmed, then the Sandbox goes,
// then the Engine VM.
package lifecycle

import (
	"fmt"
	"io"
	"slices"

	"kota/internal/interrupt"
)

// Sandbox is a started Sandbox, as *docker.Session is.
type Sandbox interface {
	Close()
	// Kill stops one of its services at once.
	Kill(service string) error
}

// Engine is a session's Engine VM, as *enginesession.Engine is.
type Engine interface {
	Start() error
	// Done is closed when the started VM exits.
	Done() <-chan struct{}
	// Ready waits for the started VM's Docker API and loads its images.
	Ready(w io.Writer) error
	Stop()
}

// Startup is how one session starts.
type Startup[S Sandbox] struct {
	// Engine is the session's Engine VM, nil without one.
	Engine Engine
	// EngineService is the Sandbox's engine forwarder.
	EngineService string
	StartSandbox  func() (S, error)
	// Configure runs once the Sandbox is up, while the Engine VM may still
	// be booting. An error stops the startup.
	Configure func(S) error
	// Verify runs once the Engine VM is ready. An error refuses the session.
	Verify func(S) error
	// Out receives warnings, such as the Engine VM stopping.
	Out io.Writer
	// Interrupted reports a signal; nil means interrupt.Err.
	Interrupted func() error
}

// Start starts the session up to where the agent or shell runs. teardown
// stops what it started, last first. On an error, nothing is left running.
func (st Startup[S]) Start() (sb S, teardown func(), err error) {
	interrupted := st.Interrupted
	if interrupted == nil {
		interrupted = interrupt.Err
	}
	var stops []func()
	stop := func() {
		for _, f := range slices.Backward(stops) {
			f()
		}
		stops = nil
	}
	defer func() {
		if err != nil {
			stop()
			var zero S
			sb = zero
		}
	}()

	if st.Engine != nil {
		if err := st.Engine.Start(); err != nil {
			return sb, nil, err
		}
		stops = append(stops, st.Engine.Stop)
	}
	if sb, err = st.StartSandbox(); err != nil {
		return sb, nil, err
	}
	stops = append(stops, sb.Close)
	if st.Engine != nil {
		// Teardown runs stops last to first, so this disarms the watch
		// before the Sandbox and the VM go.
		closing := make(chan struct{})
		stops = append(stops, func() { close(closing) })
		go st.watch(st.Engine.Done(), closing, sb)
	}
	if err := interrupted(); err != nil {
		return sb, nil, err
	}
	if st.Configure != nil {
		if err := st.Configure(sb); err != nil {
			return sb, nil, err
		}
	}
	if st.Engine != nil {
		if err := st.Engine.Ready(st.Out); err != nil {
			return sb, nil, err
		}
	}
	if err := interrupted(); err != nil {
		return sb, nil, err
	}
	if st.Verify != nil {
		if err := st.Verify(sb); err != nil {
			return sb, nil, err
		}
	}
	// A probe cut short by a signal may still have produced output.
	if err := interrupted(); err != nil {
		return sb, nil, err
	}
	return sb, stop, nil
}

// watch kills the engine forwarder when the Engine VM exits before closing.
func (st Startup[S]) watch(vmDone, closing <-chan struct{}, sb S) {
	select {
	case <-closing:
		return
	case <-vmDone:
	}
	select {
	case <-closing:
		// Both at once: the session is ending anyway.
		return
	default:
	}
	fmt.Fprintln(st.Out, "\r\nWARNING: the Engine VM stopped; its route is closed for the rest of the session")
	if err := sb.Kill(st.EngineService); err != nil {
		fmt.Fprintln(st.Out, "WARNING:", err)
	}
}

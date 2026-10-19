package check

import (
	"fmt"
	"time"

	"kota/internal/docker"
	"kota/internal/probe"
	"kota/internal/sandbox"
)

// Engine is a ready Engine VM to probe, as *enginesession.Engine is. (This
// package can't take enginesession's state: enginesession runs its probes
// through this one.)
type Engine interface {
	ProbeTargets(gw sandbox.Gateway) probe.EngineTargets
	Probes(ds *docker.Session, targets probe.EngineTargets) ([]probe.Result, error)
}

// Results are the results of both probe suites, and how long each took.
type Results struct {
	Sandbox     []probe.Result
	SandboxTime time.Duration
	// Engine is nil when the Engine VM probes didn't run.
	Engine     []probe.Result
	EngineTime time.Duration
}

// Failed are the failed probes of both suites, the Sandbox's first.
func (r Results) Failed() (sandbox, engine []probe.Result) {
	return probe.Failed(r.Sandbox), probe.Failed(r.Engine)
}

// Verify runs the Sandbox probes against ds, which must hold to expect,
// then, when engine isn't nil, the Engine VM probes through ds. engine is
// the Session's Engine VM when it is on and got ready, else nil. On an
// error, the results so far are returned with it.
func Verify(ds *docker.Session, gw sandbox.Gateway, expect sandbox.Expect, engine Engine) (Results, error) {
	return verify(func() ([]probe.Result, error) { return Run(ds, gw, expect) }, ds, gw, engine)
}

func verify(runSandbox func() ([]probe.Result, error), ds *docker.Session, gw sandbox.Gateway, engine Engine) (Results, error) {
	var r Results
	start := time.Now()
	results, err := runSandbox()
	if err != nil {
		return r, fmt.Errorf("containment probes: %w", err)
	}
	r.Sandbox, r.SandboxTime = results, time.Since(start)
	if engine == nil {
		return r, nil
	}
	start = time.Now()
	results, err = engine.Probes(ds, engine.ProbeTargets(gw))
	if err != nil {
		return r, fmt.Errorf("Engine VM probes: %w", err)
	}
	r.Engine, r.EngineTime = results, time.Since(start)
	return r, nil
}

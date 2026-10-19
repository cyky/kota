package check

import (
	"errors"
	"strings"
	"testing"

	"kota/internal/docker"
	"kota/internal/probe"
	"kota/internal/sandbox"
)

type fakeEngine struct {
	results []probe.Result
	err     error
	gw      sandbox.Gateway
	ran     bool
}

func (e *fakeEngine) ProbeTargets(gw sandbox.Gateway) probe.EngineTargets {
	e.gw = gw
	return probe.EngineTargets{Image: "probe"}
}

func (e *fakeEngine) Probes(_ *docker.Session, t probe.EngineTargets) ([]probe.Result, error) {
	e.ran = t.Image == "probe"
	return e.results, e.err
}

func results(failed ...bool) []probe.Result {
	var out []probe.Result
	for i, f := range failed {
		r := probe.Result{Probe: probe.Probe{Name: string(rune('a' + i)), MustSucceed: true}}
		if f {
			r.ExitCode = 1
		}
		out = append(out, r)
	}
	return out
}

// The Engine VM probes run after the Sandbox's, even when those failed,
// and only with an Engine VM.
func TestVerify(t *testing.T) {
	gw := sandbox.Gateway{Host: "host.test", Port: 8080}
	sandboxRun := func(rs []probe.Result, err error) func() ([]probe.Result, error) {
		return func() ([]probe.Result, error) { return rs, err }
	}

	e := &fakeEngine{results: results(false, true)}
	r, err := verify(sandboxRun(results(true, false, false), nil), nil, gw, e)
	if err != nil {
		t.Fatal(err)
	}
	if !e.ran || e.gw != gw {
		t.Errorf("engine probes ran %v, for %+v", e.ran, e.gw)
	}
	fs, fe := r.Failed()
	if len(r.Sandbox) != 3 || len(r.Engine) != 2 || len(fs) != 1 || len(fe) != 1 {
		t.Errorf("results %+v, failed %d and %d", r, len(fs), len(fe))
	}

	r, err = verify(sandboxRun(results(false), nil), nil, gw, nil)
	if err != nil || r.Engine != nil {
		t.Errorf("without an Engine VM: %+v, %v", r, err)
	}

	e = &fakeEngine{}
	_, err = verify(sandboxRun(nil, errors.New("no docker")), nil, gw, e)
	if err == nil || !strings.HasPrefix(err.Error(), "containment probes: no docker") || e.ran {
		t.Errorf("sandbox error: %v, engine ran %v", err, e.ran)
	}

	e = &fakeEngine{err: errors.New("load failed")}
	r, err = verify(sandboxRun(results(false), nil), nil, gw, e)
	if err == nil || !strings.HasPrefix(err.Error(), "Engine VM probes: load failed") || len(r.Sandbox) != 1 {
		t.Errorf("engine error: %v, results %+v", err, r)
	}
}

package enginesession

import (
	"errors"
	"strings"
	"testing"

	"kota/internal/sandbox"
)

// ForSession gives exactly one state: on when wanted and ready, can't
// start when wanted and not ready, off when not wanted but declared, and
// none for a project without an Engine VM.
func TestForSession(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		missing []string
		want    bool
		state   string
	}{
		{name: "on", yaml: yaml, want: true, state: "on"},
		{name: "can't start", yaml: yaml, missing: []string{"base"}, want: true, state: "can't start"},
		{name: "off", yaml: yaml, state: "off"},
		{name: "off, broken config of a prepped project", yaml: "engine: [\n", state: "off"},
		{name: "none, wanted", want: true, state: "none"},
		{name: "none, not wanted", state: "none"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, project := fixture(t, c.yaml, []string{"postgres:16-alpine"}, c.missing...)
			s, err := ForSession(o, project, t.TempDir(), c.want)
			if got := stateName(s); got != c.state {
				t.Fatalf("state %s, want %s", got, c.state)
			}
			if (err != nil) != (c.state == "can't start") || err != s.Err() {
				t.Errorf("err = %v, state's = %v", err, s.Err())
			}
		})
	}
}

// stateName names s, and checks it is exactly one state.
func stateName(s State) string {
	var names []string
	if s.Engine() != nil {
		names = append(names, "on")
	}
	if s.Err() != nil {
		names = append(names, "can't start")
	}
	if s.Off() {
		names = append(names, "off")
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, "+")
}

// Each state's route, note and warning.
func TestStateRouteNoteWarning(t *testing.T) {
	o, project := fixture(t, "engine:\n  images: [postgres:16-alpine, redis:7]\n", []string{"postgres:16-alpine", "redis:7"})
	on, err := ForSession(o, project, t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if r := on.Route(); r == nil || r != on.Engine().Route() {
		t.Errorf("on: route %+v", r)
	}
	if on.Warning() != on.Engine().Warning() {
		t.Errorf("on: warning %q", on.Warning())
	}
	note := on.Note()
	for _, want := range []string{
		sandbox.EngineEnv + "=" + sandbox.EngineAvailable, "DOCKER_HOST", "testcontainers",
		// The declared images, how an undeclared one fails, and the fix.
		"which you can't change: postgres:16-alpine, redis:7.", "never downloads images",
		"Unable to find image", "Temporary failure in name resolution", "find it in the failing test's code", "Don't retry",
		"under engine.images in kota.yaml and run 'kota prep' on the host",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("on: note lacks %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "not run") {
		t.Errorf("on: note says tests can't run:\n%s", note)
	}

	off := State{off: true}
	note = off.Note()
	for _, want := range []string{sandbox.EngineEnv + "=" + sandbox.EngineUnavailable, "--no-engine", "not run", "Never report them as passed"} {
		if !strings.Contains(note, want) {
			t.Errorf("off: note lacks %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "DOCKER_HOST") {
		t.Errorf("off: note offers the engine:\n%s", note)
	}

	for name, s := range map[string]State{"off": off, "none": {}, "can't start": {err: errors.New("no base disk")}} {
		if s.Route() != nil || s.Warning() != "" {
			t.Errorf("%s: route %+v, warning %q", name, s.Route(), s.Warning())
		}
		if name != "off" && s.Note() != "" {
			t.Errorf("%s: note %q", name, s.Note())
		}
	}
}

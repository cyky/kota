package enginesession

import (
	"strings"

	"kota/internal/sandbox"
)

// State is a Session's Engine VM, exactly one of:
//
//   - none: the project declares no Engine images (the zero State);
//   - off: declared, but the Session runs without it (--no-engine);
//   - can't start: declared, with the error that says why (check reports
//     it; run and shell refuse before they get a State);
//   - on: the Engine VM, prepared for the Session.
type State struct {
	engine *Engine
	err    error
	off    bool
}

// ForSession works out the Engine VM of a Session of project, whose files
// live in sessionDir. Without want, the Session runs without it: off if
// the project declares one, else none. With want, it is prepared; when it
// can't start, the State says why and the error is returned too, for a
// caller that refuses then.
func ForSession(o Options, project, sessionDir string, want bool) (State, error) {
	if !want {
		return State{off: declared(o.Layout, project)}, nil
	}
	e, err := Prepare(o, project, sessionDir)
	if err != nil {
		return State{err: err}, err
	}
	return State{engine: e}, nil
}

// Engine is the Engine VM when it is on, else nil.
func (s State) Engine() *Engine { return s.engine }

// Off reports a declared Engine VM the Session runs without.
func (s State) Off() bool { return s.off }

// Err is why the Engine VM can't start, or nil.
func (s State) Err() error { return s.err }

// Route is the Sandbox's route to the Engine VM when it is on, else nil.
func (s State) Route() *sandbox.EngineRoute {
	if s.engine == nil {
		return nil
	}
	return s.engine.Route()
}

// Warning says the Engine base disk of an Engine VM that is on should be
// rebuilt, or is "".
func (s State) Warning() string {
	if s.engine == nil {
		return ""
	}
	return s.engine.Warning()
}

// Note is the agent's instruction about the Engine VM, which matches
// sandbox.EngineEnv: whether it can run the tests that need containers,
// and, when it can't, that it must report them as not run instead of
// passed. It is "" for a project without one, or one that can't start.
// When on, it names the declared Engine images: the VM can't download any
// other image, and the pull fails with Docker's own network error, so the
// note says how to read that error and what to tell the user.
func (s State) Note() string {
	switch {
	case s.engine != nil:
		return "# Containers\n\n" +
			"This session has a container engine, the project's Engine VM (" + sandbox.EngineEnv + "=" + sandbox.EngineAvailable + "). " +
			"Docker and testcontainers use it through DOCKER_HOST, so run the project's tests that need containers as part of your checks.\n\n" +
			"It has no network and never downloads images. It has only the images declared under engine.images in kota.yaml, which you can't change: " +
			strings.Join(s.engine.Images(), ", ") + ". " +
			"A test or docker command that needs any other image fails when Docker tries to pull it, with an error such as " +
			`"Unable to find image", "lookup registry-1.docker.io", "Temporary failure in name resolution", "no such host", "i/o timeout" or another failure to reach a registry. ` +
			"That means the image isn't declared. testcontainers' error often doesn't name the image: find it in the failing test's code, or the default image of the testcontainers module it uses. " +
			"Don't retry, switch registries or rewrite the test to avoid the image. " +
			"Say which tests couldn't run because of it, and tell the user to add the image (with its tag) under engine.images in kota.yaml and run 'kota prep' on the host.\n"
	case s.off:
		return "# Containers\n\n" +
			"This session has no container engine (" + sandbox.EngineEnv + "=" + sandbox.EngineUnavailable + "). " +
			"It was started with --no-engine, so the project's Engine VM is off, and Docker and testcontainers don't work. " +
			"Tests and checks that need containers can't run in this session. " +
			"Never report them as passed: when you report your work, list them as not run, and say they need a session with the Engine VM.\n"
	}
	return ""
}

package enginesession

import (
	"strconv"
	"strings"

	"kota/internal/engine"
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
// When on, it says how a container the agent starts is reached (host
// engine, a published port in the VM's range), since a wrong route fails
// as a bare connection refused. It names the declared Engine images: the
// VM can't download any other image, and the pull fails with Docker's own
// network error, so the note says how to read that error and what to tell
// the user.
func (s State) Note() string {
	switch {
	case s.engine != nil:
		first, last := strconv.Itoa(engine.FirstPublishedPort), strconv.Itoa(engine.LastPublishedPort)
		ports := first + "-" + last
		return "# Containers\n\n" +
			"This session has a container engine, the project's Engine VM (" + sandbox.EngineEnv + "=" + sandbox.EngineAvailable + "). " +
			"Docker and testcontainers use it through DOCKER_HOST, so run the project's tests that need containers as part of your checks.\n\n" +
			"## Reaching a container\n\n" +
			"A container port is reachable from here only through the engine route: host " + sandbox.EngineHost + " on a host port in " + ports + ". " +
			"testcontainers does this itself. For a container you start, for example a database the app needs, publish its port in that range, one port per container " +
			"(`docker run -d -p " + first + ":5432 ...` for Postgres, then point the app at " + sandbox.EngineHost + ":" + first + "), " +
			"or publish it as `-p 5432`, which picks a free port in the range, and read that port with `docker port`. " +
			"Containers on a Docker network you create in the engine reach each other by name on their usual ports.\n\n" +
			"A refused or timed-out connection at localhost, at a port outside " + ports + " (such as one published with -p 5432:5432), at a port bound to 127.0.0.1 " +
			"or at a container's IP means the route, not the service: publish the port in the range on all interfaces and connect through " + sandbox.EngineHost + ".\n\n" +
			"The engine starts empty every session: start the containers you need and load their data (migrations, seeds) each time.\n\n" +
			"## Images\n\n" +
			"The engine has no network and never downloads images. It has only the images declared under engine.images in kota.yaml: " +
			strings.Join(s.engine.Images(), ", ") + ". You can't change this list. " +
			"A test or docker command that needs any other image fails when Docker tries to pull it, with an error such as " +
			`"Unable to find image", "lookup registry-1.docker.io", "Temporary failure in name resolution", "no such host", "i/o timeout" or another failure to reach a registry. ` +
			"That means the image isn't declared. testcontainers' error often doesn't name the image: find it in the failing test's code, or the default image of the testcontainers module it uses. " +
			"Treat it as final for this session: the test keeps needing that image, so leave the image it uses and its container in place, and report the missing image as the cause. " +
			"Name the tests that couldn't run and the image with its tag, " +
			"and tell the user to add it under engine.images in kota.yaml and run 'kota prep' on the host.\n"
	case s.off:
		return "# Containers\n\n" +
			"This session has no container engine (" + sandbox.EngineEnv + "=" + sandbox.EngineUnavailable + "). " +
			"It was started with --no-engine, so the project's Engine VM is off, and Docker and testcontainers don't work. " +
			"Tests and checks that need containers can't run in this session. " +
			"Never report them as passed: when you report your work, list them as not run, and say they need a session with the Engine VM.\n"
	}
	return ""
}

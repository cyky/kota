// Package offlinegit runs git against a project's repository in an offline
// container, never on the host.
//
// The agent may have written the repository's config (in a repository it
// created, or in an earlier Session) and its .gitattributes, and git runs
// the filters, fsmonitor and hooks they name. In the container whatever they
// start is offline and can write nothing but the one writable mount a job
// asks for.
package offlinegit

import (
	"slices"

	"kota/internal/docker"
	"kota/internal/pathpolicy"
)

// Job is one command to run in an offline container.
type Job struct {
	// ReadOnly are mounted read-only, each at its own host path.
	ReadOnly []string
	// Writable, unless empty, is mounted writable at its own host path.
	Writable string
	// Dir is the working directory.
	Dir string
	// Cmd is the command and its arguments.
	Cmd []string
}

// Runner runs a job and returns its stdout. A non-zero exit is an error.
type Runner func(Job) (string, error)

// Docker returns the Runner that runs each job in a throwaway container of
// image, as uid:gid. Every mount, the writable one too, must pass policy's
// --ro check: paths outside the project, such as a linked worktree's git
// dir, come from files the agent could write.
func Docker(image string, uid, gid int, policy pathpolicy.Policy) Runner {
	return func(j Job) (string, error) {
		mounts := slices.Clip(j.ReadOnly)
		if j.Writable != "" {
			mounts = append(mounts, j.Writable)
		}
		for _, m := range mounts {
			if err := policy.CheckReadOnly(m); err != nil {
				return "", err
			}
		}
		return docker.RunOfflineWritable(image, uid, gid, j.ReadOnly, j.Writable, j.Dir, j.Cmd...)
	}
}

// hardening starts every git command: no fsmonitor, no index writes, and
// no ownership check, since the container runs as the host user.
var hardening = []string{"-c", "safe.directory=*", "-c", "core.fsmonitor=false", "--no-optional-locks"}

// gitFunc defines g, git with the hardening, for scripts.
const gitFunc = `g() { git -c 'safe.directory=*' -c core.fsmonitor=false --no-optional-locks "$@"; }
`

// Git runs git with args for job, hardened, in place of job.Cmd. args may
// start with more -c options.
func Git(run Runner, job Job, args ...string) (string, error) {
	job.Cmd = append(append([]string{"git"}, hardening...), args...)
	return run(job)
}

// Script runs the sh script body for job, in place of job.Cmd, with args as
// $1 and on. body calls git as g, which adds the hardening; g takes more -c
// options before the subcommand.
func Script(run Runner, job Job, body string, args ...string) (string, error) {
	job.Cmd = append([]string{"sh", "-c", gitFunc + body, "sh"}, args...)
	return run(job)
}

// Package approval decides whether a session runs in full auto, and asks
// the user to confirm a full-auto session on a project with uncommitted
// changes, which the agent could overwrite without asking.
package approval

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Agents without a permission system of their own. The sandbox is their
// only boundary, so they always run in full auto.
var alwaysAuto = map[string]bool{"pi": true}

// FullAuto reports whether agent acts without asking: always for agents
// without permissions, otherwise only when auto was requested.
func FullAuto(agent string, auto bool) bool {
	return auto || alwaysAuto[agent]
}

// Status is the project's working tree state.
type Status struct {
	// Git is false for a project that isn't a git repository.
	Git bool
	// Changes are `git status --porcelain` lines: modified, staged and
	// untracked paths.
	Changes []string
}

// NeedsConfirmation reports whether starting a session must wait for the
// user to type y: a full-auto session in a git project with uncommitted
// changes. A non-git project has nothing to stash, so it is never asked.
func NeedsConfirmation(fullAuto bool, s Status) bool {
	return fullAuto && s.Git && len(s.Changes) > 0
}

// ReadStatus returns the working tree state of project. Only changes inside
// project count: in a subdirectory of a larger repository, the rest is out
// of the agent's reach. Optional locks and fsmonitor are off, so git
// neither writes the index nor runs a configured fsmonitor command.
func ReadStatus(project string) (Status, error) {
	inside, err := exec.Command("git", "-C", project, "rev-parse", "--is-inside-work-tree").Output()
	if err != nil || strings.TrimSpace(string(inside)) != "true" {
		return Status{}, nil
	}
	out, err := exec.Command("git", "--no-optional-locks", "-c", "core.fsmonitor=false",
		"-C", project, "status", "--porcelain", "--untracked-files=normal", "--", ".").Output()
	if err != nil {
		return Status{}, fmt.Errorf("git status: %w", err)
	}
	s := Status{Git: true}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line != "" {
			s.Changes = append(s.Changes, line)
		}
	}
	return s, nil
}

// maxListed is how many changed paths the prompt shows.
const maxListed = 10

// Confirm describes the uncommitted changes on out and reads one line from
// in. Only "y" (or "Y") continues; anything else, including end of input,
// declines.
func Confirm(in io.Reader, out io.Writer, agent string, s Status) bool {
	fmt.Fprintf(out, "%s runs in full auto: it edits files and runs commands without asking.\n", agent)
	fmt.Fprintf(out, "The project has %d uncommitted change(s) it could overwrite:\n", len(s.Changes))
	for i, c := range s.Changes {
		if i == maxListed {
			fmt.Fprintf(out, "  ... and %d more\n", len(s.Changes)-maxListed)
			break
		}
		fmt.Fprintln(out, "  "+c)
	}
	fmt.Fprint(out, "Type y to start anyway: ")
	answer := strings.TrimSpace(readLine(in))
	return answer == "y" || answer == "Y"
}

// readLine reads up to a newline one byte at a time, so nothing after the
// answer is consumed: the rest of stdin belongs to the agent.
func readLine(in io.Reader) string {
	var line []byte
	b := make([]byte, 1)
	for {
		n, err := in.Read(b)
		if n == 1 {
			if b[0] == '\n' {
				break
			}
			line = append(line, b[0])
		}
		if err != nil {
			break
		}
	}
	return string(line)
}

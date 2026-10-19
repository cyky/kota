// Package approval asks the user to confirm a full-auto session on a
// project with uncommitted changes, which the agent could overwrite without
// asking.
package approval

import (
	"fmt"
	"io"
	"strings"

	"kota/internal/interrupt"
	"kota/internal/offlinegit"
)

// NeedsConfirmation reports whether starting a session must wait for the
// user to type y: a full-auto session in a git project with uncommitted
// changes. A non-git project has nothing to stash, so it is never asked.
func NeedsConfirmation(fullAuto bool, s offlinegit.Status) bool {
	return fullAuto && s.Git && len(s.Changes) > 0
}

// maxListed is how many changed paths the prompt shows.
const maxListed = 10

// Confirm describes the uncommitted changes on out and reads one line from
// in. Only "y" (or "Y") continues; anything else, including end of input,
// declines.
func Confirm(in io.Reader, out io.Writer, agent string, s offlinegit.Status) bool {
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
	answer := strings.TrimSpace(interrupt.ReadLine(in))
	return answer == "y" || answer == "Y"
}

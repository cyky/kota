// Package probe defines the containment checks run inside a sandbox before a
// session starts, renders them as one shell script, and parses its output.
//
// Each probe is a command plus the outcome required for the sandbox to count
// as contained: must-fail probes try to escape, must-succeed probes check the
// sandbox is still usable.
package probe

import (
	"bufio"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
)

// Probe is one containment check.
type Probe struct {
	Name string
	// Cmd runs inside the agent container; its exit status is the outcome.
	Cmd []string
	// MustSucceed is true when Cmd has to exit 0, false when it has to fail.
	MustSucceed bool
}

// Result is the outcome of one probe.
type Result struct {
	Probe
	ExitCode int
	// Missing is set when the probe produced no result line.
	Missing bool
}

// Passed reports whether the probe's outcome matches its expectation.
func (r Result) Passed() bool {
	if r.Missing {
		return false
	}
	return (r.ExitCode == 0) == r.MustSucceed
}

// Targets are the addresses the network probes aim at.
type Targets struct {
	// GatewayURL is the gateway as seen from inside the sandbox.
	GatewayURL string
	// GatewayHost and GatewayPort are the forwarder's in-sandbox address.
	GatewayHost string
	GatewayPort int
	// HostAddrs are addresses that lead to the host or the Docker VM: the
	// resolved host.docker.internal, Docker network gateways, the LAN gateway.
	HostAddrs []string
	// HostPorts are tried on every host address, including the real gateway
	// port, so a direct route around the forwarder is caught.
	HostPorts []int
	// Workspace is the project mount inside the sandbox.
	Workspace string
	// ReadOnly are paths inside the sandbox that must reject writes: --ro
	// mounts, git hooks and config, .envrc.
	ReadOnly []string
	// Pinned are paths that must be mount points, so they can't be renamed
	// away and replaced.
	Pinned []string
}

// Suite returns the network, workspace and mount probes for targets.
func Suite(t Targets) []Probe {
	probes := []Probe{
		tcp("internet 1.1.1.1:443", "1.1.1.1", 443),
		tcp("internet 8.8.8.8:53", "8.8.8.8", 53),
		{Name: "internet dns example.com", Cmd: []string{"getent", "hosts", "example.com"}},
		{Name: "dns host.docker.internal", Cmd: []string{"getent", "hosts", "host.docker.internal"}},
		tcp(fmt.Sprintf("gateway other port %s:22", t.GatewayHost), t.GatewayHost, 22),
	}

	addrs := slices.Clone(t.HostAddrs)
	slices.Sort(addrs)
	for _, addr := range slices.Compact(addrs) {
		for _, port := range t.HostPorts {
			probes = append(probes, tcp("host "+net.JoinHostPort(addr, strconv.Itoa(port)), addr, port))
		}
	}

	probes = append(probes,
		Probe{
			Name:        "gateway /v1/models",
			Cmd:         []string{"curl", "-fsS", "-m", "3", "-o", "/dev/null", t.GatewayURL + "/v1/models"},
			MustSucceed: true,
		},
		Probe{
			Name:        "workspace writable",
			Cmd:         []string{"sh", "-c", `f="$1/.localllm-probe-$$" && : > "$f" && rm -f "$f"`, "sh", t.Workspace},
			MustSucceed: true,
		},
	)
	for _, path := range t.ReadOnly {
		// A missing mount exits 0, so it fails the probe rather than passing
		// because there is nothing to write to.
		probes = append(probes, Probe{
			Name: "read-only " + path,
			Cmd: []string{"sh", "-c", `[ -e "$1" ] || exit 0
if [ -d "$1" ]; then f="$1/.localllm-probe-$$" && : > "$f" && rm -f "$f"; else : >> "$1"; fi`, "sh", path},
		})
	}
	for _, path := range t.Pinned {
		probes = append(probes, Probe{
			Name:        "pinned " + path,
			Cmd:         []string{"awk", "-v", "p=" + path, `$5 == p { found = 1 } END { exit !found }`, "/proc/self/mountinfo"},
			MustSucceed: true,
		})
	}
	return probes
}

func tcp(name, host string, port int) Probe {
	return Probe{
		Name: name,
		Cmd:  []string{"bash", "-c", `exec 3<>"/dev/tcp/$1/$2"`, "bash", host, strconv.Itoa(port)},
	}
}

// Timeout bounds each probe, in seconds. Blocked connections on an internal
// network fail immediately; the timeout only matters for silent drops.
const Timeout = 2

const resultPrefix = "LOCALLLM-PROBE"

// Script renders probes as a bash script that runs them in parallel and prints
// one result line per probe: LOCALLLM-PROBE <index> <exit code>.
func Script(probes []Probe) string {
	var b strings.Builder
	b.WriteString("probe() { i=$1; shift; timeout " + strconv.Itoa(Timeout) + " \"$@\" </dev/null >/dev/null 2>&1; echo \"" + resultPrefix + " $i $?\"; }\n")
	for i, p := range probes {
		fmt.Fprintf(&b, "probe %d", i)
		for _, arg := range p.Cmd {
			b.WriteString(" " + shellQuote(arg))
		}
		b.WriteString(" &\n")
	}
	b.WriteString("wait\n")
	return b.String()
}

// Parse matches the script's output back to probes. Probes without a result
// line are reported as Missing, so a crashed script never looks like a pass.
func Parse(probes []Probe, output string) []Result {
	results := make([]Result, len(probes))
	for i, p := range probes {
		results[i] = Result{Probe: p, Missing: true}
	}
	sc := bufio.NewScanner(strings.NewReader(output))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 3 || fields[0] != resultPrefix {
			continue
		}
		i, err1 := strconv.Atoi(fields[1])
		code, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil || i < 0 || i >= len(probes) {
			continue
		}
		results[i].ExitCode = code
		results[i].Missing = false
	}
	return results
}

// Failed returns the results that did not pass.
func Failed(results []Result) []Result {
	var failed []Result
	for _, r := range results {
		if !r.Passed() {
			failed = append(failed, r)
		}
	}
	return failed
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

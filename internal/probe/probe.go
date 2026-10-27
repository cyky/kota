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

	"kota/internal/sandbox"
)

// Probe is one containment check.
type Probe struct {
	Name string
	// Cmd runs inside the agent container; its exit status is the outcome.
	Cmd []string
	// MustSucceed is true when Cmd has to exit 0, false when it has to fail.
	MustSucceed bool
	// InEngine runs Cmd in a probe container inside the Engine VM instead.
	InEngine bool
}

// Result is the outcome of one probe.
type Result struct {
	Probe
	ExitCode int
	// Missing is set when the probe produced no result line.
	Missing bool
}

// Passed reports whether the probe's outcome matches its expectation. A
// must-fail probe whose command couldn't run (exit 126 or 127) fails: a
// missing tool proves nothing is blocked.
func (r Result) Passed() bool {
	if r.Missing {
		return false
	}
	if !r.MustSucceed && (r.ExitCode == 126 || r.ExitCode == 127) {
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
	// Masked are paths that must read as an empty file or directory.
	Masked []string
	// State is the agent's state directory, which must be writable so
	// sessions persist. Empty skips the probe.
	State string
	// Mounts are the mount points kota gives the agent container. Any other
	// mount, besides the container runtime's own, fails the probe.
	Mounts []string
}

// metadataAddr is the cloud instance metadata service, which hands out the
// machine's credentials on most clouds.
const metadataAddr = "169.254.169.254"

// runtimeMounts matches the mount points a container runtime adds: the
// root, /proc, /sys and /dev with what is under them, the files Docker
// writes for name resolution, and its init binary when the daemon runs
// containers with "init": true.
const runtimeMounts = `$5 == "/" || $5 ~ /^\/(proc|sys|dev)(\/|$)/ || $5 ~ /^\/etc\/(hosts|hostname|resolv\.conf)$/ || $5 == "/sbin/docker-init"`

// Suite returns the network, workspace and mount probes for targets.
func Suite(t Targets) []Probe {
	probes := []Probe{
		tcp("internet 1.1.1.1:443", "1.1.1.1", 443),
		tcp("internet 8.8.8.8:53", "8.8.8.8", 53),
		tcp("internet [2606:4700:4700::1111]:443", "2606:4700:4700::1111", 443),
		udpDNS("internet dns udp 8.8.8.8:53", "8.8.8.8"),
		udpDNS("internet dns udp [2606:4700:4700::1111]:53", "2606:4700:4700::1111"),
		tcp("cloud metadata "+metadataAddr+":80", metadataAddr, 80),
		{Name: "internet dns example.com", Cmd: []string{"getent", "hosts", "example.com"}},
		{Name: "internet dns ipv6 example.com", Cmd: []string{"getent", "ahostsv6", "example.com"}},
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
			Name: "gateway /v1/models",
			// Sends the gateway key when the container has one.
			Cmd: []string{"sh", "-c", `url=$1; shift
if [ -n "$` + sandbox.GatewayKeyEnv + `" ]; then set -- -H "Authorization: Bearer $` + sandbox.GatewayKeyEnv + `"; fi
exec curl -fsS -m 3 -o /dev/null "$@" "$url"`, "sh", t.GatewayURL + "/v1/models"},
			MustSucceed: true,
		},
		Probe{
			Name:        "workspace writable",
			Cmd:         []string{"sh", "-c", `f="$1/.kota-probe-$$" && : > "$f" && rm -f "$f"`, "sh", t.Workspace},
			MustSucceed: true,
		},
	)
	if t.State != "" {
		probes = append(probes, Probe{
			Name:        "state writable",
			Cmd:         []string{"sh", "-c", `f="$1/.kota-probe-$$" && : > "$f" && rm -f "$f"`, "sh", t.State},
			MustSucceed: true,
		})
	}
	probes = append(probes,
		Probe{
			// No capabilities in any set, no_new_privs, and no root UID.
			Name: "privileges dropped",
			Cmd: []string{"awk", `/^Cap(Inh|Prm|Eff|Bnd|Amb):/ && $2 !~ /^0+$/ { bad = 1 }
/^Uid:/ { for (i = 2; i <= 5; i++) if ($i == 0) bad = 1 }
/^NoNewPrivs:/ { nnp = $2 }
END { exit bad || nnp != 1 }`, "/proc/self/status"},
			MustSucceed: true,
		},
		Probe{
			// Escaped mount points go through the environment, one per line,
			// as the pinned probes' do.
			Name: "no unexpected mounts",
			Cmd: []string{"env", "KOTA_MOUNTS=" + mountList(t.Mounts), "awk", `BEGIN { n = split(ENVIRON["KOTA_MOUNTS"], m, "\n"); for (i = 1; i <= n; i++) ok[m[i]] = 1 }
($5 in ok) || ` + runtimeMounts + ` { next }
{ bad = 1 }
END { exit bad }`, "/proc/self/mountinfo"},
			MustSucceed: true,
		},
	)
	for _, path := range t.ReadOnly {
		// A missing mount exits 0, so it fails the probe rather than passing
		// because there is nothing to write to.
		probes = append(probes, Probe{
			Name: "read-only " + path,
			Cmd: []string{"sh", "-c", `[ -e "$1" ] || exit 0
if [ -d "$1" ]; then f="$1/.kota-probe-$$" && : > "$f" && rm -f "$f"; else : >> "$1"; fi`, "sh", path},
		})
	}
	for _, path := range t.Masked {
		probes = append(probes, Probe{
			Name:        "masked " + path,
			Cmd:         []string{"sh", "-c", `if [ -d "$1" ]; then [ -z "$(ls -A "$1")" ]; else [ -f "$1" ] && [ ! -s "$1" ]; fi`, "sh", path},
			MustSucceed: true,
		})
	}
	for _, path := range t.Pinned {
		// The path goes through the environment: awk -v would interpret
		// backslashes in it.
		probes = append(probes, Probe{
			Name:        "pinned " + path,
			Cmd:         []string{"env", "KOTA_PIN=" + MountinfoEscape(path), "awk", `$5 == ENVIRON["KOTA_PIN"] { found = 1 } END { exit !found }`, "/proc/self/mountinfo"},
			MustSucceed: true,
		})
	}
	return probes
}

// MountinfoEscape escapes path as the kernel writes it in the mount point
// field of /proc/self/mountinfo: space, tab, newline and backslash become
// octal escapes.
func MountinfoEscape(path string) string {
	var b strings.Builder
	for _, c := range []byte(path) {
		switch c {
		case ' ', '\t', '\n', '\\':
			fmt.Fprintf(&b, `\%03o`, c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// mountList is paths escaped as in /proc/self/mountinfo, one per line.
func mountList(paths []string) string {
	escaped := make([]string, len(paths))
	for i, p := range paths {
		escaped[i] = MountinfoEscape(p)
	}
	return strings.Join(escaped, "\n")
}

func tcp(name, host string, port int) Probe {
	return Probe{
		Name: name,
		Cmd:  []string{"bash", "-c", `exec 3<>"/dev/tcp/$1/$2"`, "bash", host, strconv.Itoa(port)},
	}
}

// dnsQuery is a DNS query for example.com's A record, with the ID "ko".
const dnsQuery = `\x6b\x6f\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x07example\x03com\x00\x00\x01\x00\x01`

// udpDNS sends dnsQuery to server's port 53 over UDP and succeeds only on
// a reply, whose first byte is the ID's. UDP has no connection to refuse,
// so the reply is the proof.
func udpDNS(name, server string) Probe {
	return Probe{
		Name: name,
		Cmd: []string{"bash", "-c", `exec 3<>"/dev/udp/$1/$2" && printf "$3" >&3 && IFS= read -r -t 1 -n 1 c <&3 && [ "$c" = k ]`,
			"bash", server, "53", dnsQuery},
	}
}

// Timeout bounds each probe, in seconds. Blocked connections on an internal
// network fail immediately; the timeout only matters for silent drops.
const Timeout = 2

const resultPrefix = "KOTA-PROBE"

// Script renders probes as a bash script that runs them in parallel and prints
// one result line per probe: KOTA-PROBE <index> <exit code>.
func Script(probes []Probe) string {
	var all []int
	for i := range probes {
		all = append(all, i)
	}
	return script(probes, all, true)
}

// script renders the probes at indices, numbered by their index, in
// parallel or one after the other. It is POSIX sh, so it also runs in
// busybox.
func script(probes []Probe, indices []int, parallel bool) string {
	var b strings.Builder
	b.WriteString("probe() { i=$1; shift; timeout " + strconv.Itoa(Timeout) + " \"$@\" </dev/null >/dev/null 2>&1; echo \"" + resultPrefix + " $i $?\"; }\n")
	for _, i := range indices {
		fmt.Fprintf(&b, "probe %d", i)
		for _, arg := range probes[i].Cmd {
			b.WriteString(" " + shellQuote(arg))
		}
		if parallel {
			b.WriteString(" &")
		}
		b.WriteString("\n")
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

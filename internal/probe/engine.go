package probe

import (
	"net"
	"slices"
	"strconv"
	"strings"
)

// EngineTargets are what the Engine VM probes aim at. The in-VM probes run
// in a container of Image on the VM's own network, so they test what root
// in the VM could reach; the rest run in the sandbox like Suite's probes.
type EngineTargets struct {
	// Image runs the in-VM probes and the published-port listener. It
	// needs sh, timeout, socat and nslookup, and is loaded into the VM
	// for the probes only.
	Image string
	// GuestAPIPort is dockerd's port inside the VM: the in-VM control.
	GuestAPIPort int
	// HostAddrs lead to the host from inside the VM: QEMU's host and DNS
	// aliases, the host's own addresses and the LAN gateway.
	HostAddrs []string
	// HostPorts are tried on every host address, including the gateway's
	// and the host side of the VM's own forwards.
	HostPorts []int
	// EngineHost and APIPort are the engine forwarder's address in the
	// sandbox, and CertDir the client certificate's directory there.
	EngineHost string
	APIPort    int
	CertDir    string
}

const (
	// listenerName is the published-port probe's container in the VM.
	listenerName = "kota-engine-probe"
	// listenerPort is the listener's container port, published to the
	// VM's published range.
	listenerPort = 8080
	// listenerGreeting is what the listener sends to each connection.
	listenerGreeting = "kota-engine-probe"
	// publishedEnv holds the listener's published port in the script.
	publishedEnv = "KOTA_PROBE_PUBLISHED"
)

// EngineSuite returns the Engine VM's probes. Inside the VM, everything
// but the VM itself must be unreachable. From the sandbox, the API must
// answer with the session's client certificate and only with it, and a
// port published by a container in the VM must answer.
func EngineSuite(t EngineTargets) []Probe {
	probes := []Probe{
		// The control: the probe container works and sees the VM's network.
		engineTCP("engine vm itself 127.0.0.1:"+strconv.Itoa(t.GuestAPIPort), "127.0.0.1", t.GuestAPIPort, true),
		engineTCP("engine internet 1.1.1.1:443", "1.1.1.1", 443, false),
		engineTCP("engine internet 8.8.8.8:53", "8.8.8.8", 53, false),
		engineTCP("engine internet [2606:4700:4700::1111]:443", "2606:4700:4700::1111", 443, false),
		engineTCP("engine cloud metadata "+metadataAddr+":80", metadataAddr, 80, false),
		{Name: "engine dns example.com", Cmd: []string{"nslookup", "example.com"}, InEngine: true},
		{Name: "engine dns example.com via 10.0.2.3", Cmd: []string{"nslookup", "example.com", "10.0.2.3"}, InEngine: true},
		{Name: "engine dns example.com via 1.1.1.1", Cmd: []string{"nslookup", "example.com", "1.1.1.1"}, InEngine: true},
	}
	addrs := slices.Clone(t.HostAddrs)
	slices.Sort(addrs)
	ports := slices.Clone(t.HostPorts)
	slices.Sort(ports)
	for _, addr := range slices.Compact(addrs) {
		for _, port := range slices.Compact(ports) {
			probes = append(probes, engineTCP("engine host "+net.JoinHostPort(addr, strconv.Itoa(port)), addr, port, false))
		}
	}
	api := "https://" + net.JoinHostPort(t.EngineHost, strconv.Itoa(t.APIPort)) + "/_ping"
	ca := t.CertDir + "/ca.pem"
	probes = append(probes,
		Probe{
			Name:        "engine API with the session's certificate",
			Cmd:         []string{"curl", "-fsS", "-m", "2", "--cacert", ca, "--cert", t.CertDir + "/cert.pem", "--key", t.CertDir + "/key.pem", api},
			MustSucceed: true,
		},
		Probe{
			Name: "engine API without a certificate",
			Cmd:  []string{"curl", "-fsS", "-m", "2", "--cacert", ca, api},
		},
		Probe{
			Name: "engine published port",
			Cmd: []string{"bash", "-c", `[ -n "$` + publishedEnv + `" ] || exit 1
exec 3<>"/dev/tcp/$1/$` + publishedEnv + `" && read -r -t 1 line <&3 && [ "$line" = "$2" ]`, "bash", t.EngineHost, listenerGreeting},
			MustSucceed: true,
		},
	)
	return probes
}

// engineTCP connects from inside the VM. socat exits 0 once connected.
func engineTCP(name, host string, port int, mustSucceed bool) Probe {
	addr := "TCP:" + net.JoinHostPort(host, strconv.Itoa(port))
	if strings.Contains(host, ":") {
		addr = "TCP6:" + net.JoinHostPort(host, strconv.Itoa(port))
	}
	return Probe{
		Name:        name,
		Cmd:         []string{"socat", "-T1", "/dev/null", addr + ",connect-timeout=" + strconv.Itoa(Timeout)},
		MustSucceed: mustSucceed,
		InEngine:    true,
	}
}

// EngineScript renders probes from EngineSuite as a bash script for the
// sandbox, with the session's Docker environment. It starts the listener,
// runs the in-VM probes in one container on the VM's network, then the
// sandbox probes, then removes the listener and the probe image, which
// were never declared. Output lines are as Script's.
func EngineScript(probes []Probe, image string) string {
	var inVM, local []int
	for i, p := range probes {
		if p.InEngine {
			inVM = append(inVM, i)
		} else {
			local = append(local, i)
		}
	}
	q := shellQuote
	var b strings.Builder
	b.WriteString("docker run -d --pull never --name " + listenerName + " -p " + strconv.Itoa(listenerPort) + " --entrypoint socat " + q(image) +
		" TCP-LISTEN:" + strconv.Itoa(listenerPort) + ",fork,reuseaddr " + q("SYSTEM:echo "+listenerGreeting) + " >/dev/null 2>&1\n")
	b.WriteString("export " + publishedEnv + "=$(docker port " + listenerName + " " + strconv.Itoa(listenerPort) + "/tcp 2>/dev/null | sed -n '1s/.*://p')\n")
	if len(inVM) > 0 {
		b.WriteString("docker run --rm --pull never --network host --entrypoint sh " + q(image) + " -c " + q(script(probes, inVM, true)) + "\n")
	}
	// QEMU's port forwards drop some connections in a burst of parallel
	// ones, so the sandbox's probes run one at a time, after the in-VM
	// container is done with the API.
	b.WriteString(script(probes, local, false))
	b.WriteString("docker rm -f " + listenerName + " >/dev/null 2>&1\n")
	b.WriteString("docker rmi " + q(image) + " >/dev/null 2>&1\n")
	return b.String()
}

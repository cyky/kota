// Package check runs the containment probes against a started sandbox.
package check

import (
	"net"
	"net/netip"
	"os/exec"
	"runtime"
	"slices"
	"strings"

	"localllm/internal/docker"
	"localllm/internal/probe"
	"localllm/internal/sandbox"
)

// extraHostPorts are tried on every host address besides the gateway port:
// ssh, http(s), the Docker API and Kubernetes, which a Docker VM may expose.
var extraHostPorts = []int{22, 80, 443, 2375, 6443}

// Mounts are the in-sandbox paths whose mounts the probes verify.
type Mounts struct {
	// ReadOnly must reject writes.
	ReadOnly []string
	// Pinned must be mount points.
	Pinned []string
}

// Run discovers the addresses that lead out of the sandbox, runs the probe
// suite in a fresh agent container and returns every result.
func Run(s *docker.Session, gw sandbox.Gateway, m Mounts) ([]probe.Result, error) {
	t := Targets(s, gw)
	t.ReadOnly = m.ReadOnly
	t.Pinned = m.Pinned
	probes := probe.Suite(t)
	out, err := s.Capture("bash", "-c", probe.Script(probes))
	if err != nil {
		return nil, err
	}
	return probe.Parse(probes, out), nil
}

// Targets collects host-side addresses the agent must not reach. Discovery
// failures only shrink the list; the fixed internet probes always run.
func Targets(s *docker.Session, gw sandbox.Gateway) probe.Targets {
	var addrs []string
	if ip := net.ParseIP(gw.Host); ip != nil {
		addrs = append(addrs, gw.Host)
	} else if resolved, err := s.ResolveFromForwarder(gw.Host); err == nil {
		addrs = append(addrs, resolved...)
	}
	if resolved, err := s.ResolveFromForwarder("host.docker.internal"); err == nil {
		addrs = append(addrs, resolved...)
	}
	if gws, err := s.NetworkGateways(); err == nil {
		addrs = append(addrs, gws...)
	}
	if prefixes, err := s.VMInterfaces(); err == nil {
		addrs = append(addrs, vmAddrs(prefixes)...)
	}
	if lan := lanGateway(); lan != "" {
		addrs = append(addrs, lan)
	}

	ports := append([]int{gw.Port}, extraHostPorts...)
	slices.Sort(ports)

	return probe.Targets{
		GatewayURL:  sandbox.AgentGatewayURL(),
		GatewayHost: sandbox.GatewayHost,
		GatewayPort: sandbox.GatewayPort,
		HostAddrs:   addrs,
		HostPorts:   slices.Compact(ports),
		Workspace:   sandbox.Workspace,
	}
}

// vmAddrs returns each non-loopback VM interface address plus the first
// address of its subnet, which is typically the host side of that link.
func vmAddrs(prefixes []netip.Prefix) []string {
	var addrs []string
	for _, p := range prefixes {
		if p.Addr().IsLoopback() || !p.Addr().Is4() {
			continue
		}
		addrs = append(addrs, p.Addr().String())
		if first := p.Masked().Addr().Next(); first != p.Addr() {
			addrs = append(addrs, first.String())
		}
	}
	return addrs
}

// lanGateway returns the host's default route gateway, or "" if unknown.
func lanGateway() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("route", "-n", "get", "default").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "gateway:"); ok {
			v = strings.TrimSpace(v)
			if net.ParseIP(v) != nil {
				return v
			}
		}
	}
	return ""
}

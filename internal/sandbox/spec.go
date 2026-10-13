// Package sandbox turns session inputs into a docker compose spec.
//
// The agent container is attached only to an internal network with no route
// out. A forwarder container joins both that network and a normal one and
// forwards a single port to the model gateway, which the agent reaches as
// http://gateway:8080.
package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	// GatewayHost is the forwarder's alias on the internal network.
	GatewayHost = "gateway"
	// GatewayPort is the port the forwarder listens on.
	GatewayPort = 8080
	// Workspace is where the project is mounted inside the agent container.
	Workspace = "/workspace"
	// Home is the agent user's HOME, backed by a tmpfs.
	Home = "/home/agent"

	// AgentService is the compose service name of the agent container.
	AgentService = "agent"
	// ForwarderService is the compose service name of the forwarder.
	ForwarderService = "forwarder"

	internalNetwork = "sandbox"
	egressNetwork   = "egress"
)

// Gateway is the host-side model gateway the forwarder connects to.
type Gateway struct {
	Host string
	Port int
}

// ParseGateway parses a gateway URL such as http://host.docker.internal:8080.
// Only plain http is supported: the forwarder is a TCP relay, and the gateway
// lives on the host's loopback.
func ParseGateway(raw string) (Gateway, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Gateway{}, fmt.Errorf("parse gateway URL %q: %w", raw, err)
	}
	if u.Scheme != "http" {
		return Gateway{}, fmt.Errorf("gateway URL %q: scheme must be http", raw)
	}
	if u.Hostname() == "" {
		return Gateway{}, fmt.Errorf("gateway URL %q: missing host", raw)
	}
	if u.Path != "" && u.Path != "/" {
		return Gateway{}, fmt.Errorf("gateway URL %q: must not have a path", raw)
	}
	port := 80
	if p := u.Port(); p != "" {
		port, err = strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return Gateway{}, fmt.Errorf("gateway URL %q: invalid port", raw)
		}
	}
	return Gateway{Host: u.Hostname(), Port: port}, nil
}

// AgentGatewayURL is the gateway base URL as seen from inside the sandbox.
func AgentGatewayURL() string {
	return fmt.Sprintf("http://%s:%d", GatewayHost, GatewayPort)
}

// Mount is a read-only bind mount of a host file or directory.
type Mount struct {
	Source string
	Target string
}

// Inputs describes one sandbox session.
type Inputs struct {
	// ProjectDir is the resolved absolute host path mounted at Workspace.
	ProjectDir string
	UID, GID   int
	Gateway    Gateway

	AgentImage     string
	ForwarderImage string

	// ReadOnly are extra read-only mounts, e.g. the rendered agent config.
	ReadOnly []Mount
	// Env is passed to the agent container in addition to HOME.
	Env map[string]string
	// Command overrides the agent image's default command when non-empty.
	Command []string
}

// ProjectName returns a stable compose project name for a project directory.
func ProjectName(projectDir string) string {
	sum := sha256.Sum256([]byte(projectDir))
	base := strings.ToLower(filepath.Base(projectDir))
	base = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "project"
	}
	return fmt.Sprintf("localllm-%s-%s", base, hex.EncodeToString(sum[:])[:8])
}

// Build produces the compose spec for a session.
func Build(in Inputs) Compose {
	hardening := Service{
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges:true"},
	}

	forwarder := hardening
	forwarder.Image = in.ForwarderImage
	forwarder.ReadOnly = true
	forwarder.Command = []string{
		fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr", GatewayPort),
		"TCP:" + net.JoinHostPort(in.Gateway.Host, strconv.Itoa(in.Gateway.Port)),
	}
	forwarder.Networks = map[string]*NetworkAttach{
		internalNetwork: {Aliases: []string{GatewayHost}},
		egressNetwork:   {},
	}

	env := map[string]string{"HOME": Home}
	for k, v := range in.Env {
		env[k] = v
	}

	volumes := []Volume{{Type: "bind", Source: in.ProjectDir, Target: Workspace}}
	for _, m := range in.ReadOnly {
		volumes = append(volumes, Volume{Type: "bind", Source: m.Source, Target: m.Target, ReadOnly: true})
	}

	agent := hardening
	agent.Image = in.AgentImage
	agent.User = fmt.Sprintf("%d:%d", in.UID, in.GID)
	agent.WorkingDir = Workspace
	agent.Environment = env
	agent.Volumes = volumes
	agent.Tmpfs = []string{fmt.Sprintf("%s:uid=%d,gid=%d,mode=0700", Home, in.UID, in.GID)}
	agent.Networks = map[string]*NetworkAttach{internalNetwork: {}}
	agent.DependsOn = []string{ForwarderService}
	agent.StdinOpen = true
	agent.Tty = true
	if len(in.Command) > 0 {
		agent.Command = in.Command
	}

	return Compose{
		Name: ProjectName(in.ProjectDir),
		Services: map[string]Service{
			AgentService:     agent,
			ForwarderService: forwarder,
		},
		Networks: map[string]Network{
			// Isolated gateway mode leaves the bridge without an address, so
			// the Docker VM's own services (e.g. its sshd) can't be reached
			// from the internal network. "internal" alone only blocks routing.
			internalNetwork: {Internal: true, DriverOpts: map[string]string{
				"com.docker.network.bridge.gateway_mode_ipv4": "isolated",
			}},
			egressNetwork: {},
		},
	}
}

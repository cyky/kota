// Package engine holds the pure parts of a project's Engine VM: its config
// in kota.yaml, its QEMU command line, its TLS certificates, its image
// archives and the definition of the Engine base disk it boots from.
//
// The Engine VM runs the containers the agent starts during a session (for
// example testcontainers). It is locked down from outside the VM by QEMU's
// user-mode networking with restrict=on, boots every session from the
// Engine base disk through a throwaway layer, and serves the Docker API
// only to clients with the session's TLS certificate.
package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ConfigFile is the per-project config that holds the engine section.
const ConfigFile = "kota.yaml"

const (
	// DefaultCPUs and DefaultMemoryMiB size the Engine VM. It runs next to
	// the local model, so it stays small.
	DefaultCPUs      = 2
	DefaultMemoryMiB = 2048
)

// Config is a project's engine section.
type Config struct {
	// Images are the Engine images, as declared: plain references with an
	// optional tag, such as postgres:16-alpine.
	Images []string
	// CPUs and MemoryMiB size the Engine VM: the section's cpus and memory,
	// or the defaults.
	CPUs      int
	MemoryMiB int
}

// MinMemoryMiB is the smallest Engine VM: Docker and a database fit in it.
const MinMemoryMiB = 512

// keys are the engine section's keys, in the order error messages list them.
var keys = []string{"images", "cpus", "memory"}

// Read parses the engine section of the project's ConfigFile. It returns
// nil when the file or the section is absent: the project has no Engine VM.
func Read(project string) (*Config, error) {
	data, err := os.ReadFile(filepath.Join(project, ConfigFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse parses the engine section of a ConfigFile's content. Other sections
// are left to their own parsers. An absent section returns nil.
func Parse(data []byte) (*Config, error) {
	var file struct {
		Engine yaml.Node `yaml:"engine"`
	}
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", ConfigFile, err)
	}
	node := &file.Engine
	if node.Kind == 0 {
		return nil, nil
	}
	if node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	cfg := &Config{CPUs: DefaultCPUs, MemoryMiB: DefaultMemoryMiB}
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		node = &yaml.Node{Kind: yaml.MappingNode}
	} else if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: engine: want keys %s", ConfigFile, strings.Join(keys, ", "))
	}
	// The section's keys are checked here, so a typo in it is rejected while
	// other sections' keys are left to their own parsers.
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i].Value, node.Content[i+1]
		var err error
		switch key {
		case "images":
			cfg.Images, err = parseImages(value)
		case "cpus":
			cfg.CPUs, err = parseCPUs(value)
		case "memory":
			cfg.MemoryMiB, err = parseMemory(value)
		default:
			return nil, fmt.Errorf("%s: engine: unknown key %q (want %s)", ConfigFile, key, strings.Join(keys, ", "))
		}
		if err != nil {
			return nil, fmt.Errorf("%s: engine.%s: %w", ConfigFile, key, err)
		}
	}
	if len(cfg.Images) == 0 {
		return nil, fmt.Errorf("%s: engine.images: declare at least one image, or remove the engine section", ConfigFile)
	}
	return cfg, nil
}

func parseImages(node *yaml.Node) ([]string, error) {
	var images []string
	if err := node.Decode(&images); err != nil {
		return nil, fmt.Errorf("want a list of image references, such as [postgres:16-alpine]")
	}
	seen := map[string]bool{}
	for _, ref := range images {
		if err := checkRef(ref); err != nil {
			return nil, err
		}
		if seen[ref] {
			return nil, fmt.Errorf("%q is declared twice", ref)
		}
		seen[ref] = true
	}
	return images, nil
}

func parseCPUs(node *yaml.Node) (int, error) {
	var n int
	if node.Kind != yaml.ScalarNode || node.Tag != "!!int" || node.Decode(&n) != nil || n < 1 {
		return 0, fmt.Errorf("%q: want a whole number of CPUs of at least 1, such as 4", node.Value)
	}
	return n, nil
}

// memorySize is a size in MiB or GiB, as QEMU's -m takes it.
var memorySize = regexp.MustCompile(`^([0-9]{1,7}) ?(M|MiB|G|GiB)$`)

func parseMemory(node *yaml.Node) (int, error) {
	m := memorySize.FindStringSubmatch(node.Value)
	if node.Kind != yaml.ScalarNode || m == nil {
		return 0, fmt.Errorf("%q: want a size with a unit, such as 4G or 3072M", node.Value)
	}
	n, _ := strconv.Atoi(m[1])
	if m[2] == "G" || m[2] == "GiB" {
		n *= 1024
	}
	if n < MinMemoryMiB {
		return 0, fmt.Errorf("%q: want at least %dM", node.Value, MinMemoryMiB)
	}
	return n, nil
}

// reference is Docker's image reference grammar without a digest: an
// optional registry host, path components and an optional tag.
var reference = regexp.MustCompile(`^` +
	`(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?)*(?::[0-9]+)?/)?` +
	`[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*` +
	`(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?$`)

func checkRef(ref string) error {
	if strings.Contains(ref, "@") {
		return fmt.Errorf("%q: use a tag, not a digest; prep records the digest the tag resolves to", ref)
	}
	if len(ref) > 255 || !reference.MatchString(ref) {
		return fmt.Errorf("%q is not a valid image reference (want e.g. postgres:16-alpine)", ref)
	}
	return nil
}

// SplitRef splits a declared reference into its repository and tag. A
// reference without a tag gets "latest", as Docker gives it.
func SplitRef(ref string) (repo, tag string) {
	slash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref, ":"); colon > slash {
		return ref[:colon], ref[colon+1:]
	}
	return ref, "latest"
}

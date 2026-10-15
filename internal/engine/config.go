// Package engine holds the pure parts of a project's Engine VM: its config
// in localllm.yaml, its QEMU command line, its TLS certificates, its image
// archives and the definition of the Engine base disk it boots from.
//
// The Engine VM runs the containers the agent starts during a session (for
// example testcontainers). It is locked down from outside the VM by QEMU's
// user-mode networking with restrict=on, boots every session from the
// Engine base disk through a throwaway layer, and serves the Docker API
// only to clients with the session's TLS certificate.
package engine

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ConfigFile is the per-project config that holds the engine section.
const ConfigFile = "localllm.yaml"

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
	Images    []string
	CPUs      int
	MemoryMiB int
}

// section is the engine section as written in ConfigFile.
type section struct {
	Images []string `yaml:"images"`
}

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
	if file.Engine.Kind == 0 {
		return nil, nil
	}
	// Decode the section on its own, so unknown keys in it are rejected
	// while other sections' keys are not.
	raw, err := yaml.Marshal(&file.Engine)
	if err != nil {
		return nil, fmt.Errorf("parse %s: engine: %w", ConfigFile, err)
	}
	var s section
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse %s: engine: %w", ConfigFile, err)
	}
	if len(s.Images) == 0 {
		return nil, fmt.Errorf("%s: engine.images: declare at least one image, or remove the engine section", ConfigFile)
	}
	seen := map[string]bool{}
	for _, ref := range s.Images {
		if err := checkRef(ref); err != nil {
			return nil, fmt.Errorf("%s: engine.images: %w", ConfigFile, err)
		}
		if seen[ref] {
			return nil, fmt.Errorf("%s: engine.images: %q is declared twice", ConfigFile, ref)
		}
		seen[ref] = true
	}
	return &Config{Images: s.Images, CPUs: DefaultCPUs, MemoryMiB: DefaultMemoryMiB}, nil
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

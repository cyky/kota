// Package gatewayconfig picks the model Gateway for one command: a name
// from the user's config.yaml, a URL, or the built-in default. The config
// belongs to the user, never to a project (ADR 0002).
package gatewayconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"kota/internal/sandbox"
)

const (
	// File is the config's name in the kota config dir.
	File = "config.yaml"
	// Env picks the gateway when --gateway isn't given.
	Env = "KOTA_GATEWAY"
	// DefaultURL is the gateway without a flag, Env or defaultGateway:
	// llama-swap on the host.
	DefaultURL = "http://host.docker.internal:8686"
)

// Sources of the choice, for the user.
const (
	SourceFlag    = "--gateway"
	SourceEnv     = Env
	SourceDefault = "defaultGateway"
	SourceBuiltin = "built-in default"
)

// Choice is the gateway one command uses.
type Choice struct {
	// Name is the gateway's name in the config, empty for a URL.
	Name string
	// URL is the gateway's URL as written.
	URL     string
	Gateway sandbox.Gateway
	// Source says where the choice came from: one of the Source constants.
	Source string
}

// String is the choice as kota shows it: name, URL and source.
func (c Choice) String() string {
	if c.Name == "" {
		return fmt.Sprintf("%s (%s)", c.URL, c.Source)
	}
	return fmt.Sprintf("%s %s (%s)", c.Name, c.URL, c.Source)
}

// config is the parsed config.yaml.
type config struct {
	DefaultGateway string             `yaml:"defaultGateway"`
	Gateways       map[string]gateway `yaml:"gateways"`
}

type gateway struct {
	URL string `yaml:"url"`
}

// validName keeps names apart from URLs and readable in output.
var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Resolve reads dir/config.yaml, if there is one, checks all of it, and
// picks the gateway: flag (the --gateway value, empty when not given), then
// Env, then the config's defaultGateway, then DefaultURL. A value with
// "://" is a URL, anything else a gateway name. getenv is os.Getenv outside
// tests.
func Resolve(dir, flag string, getenv func(string) string) (Choice, error) {
	path := filepath.Join(dir, File)
	cfg, err := load(path)
	if err != nil {
		return Choice{}, err
	}
	value, source := flag, SourceFlag
	if value == "" {
		value, source = getenv(Env), SourceEnv
	}
	if value == "" {
		value, source = cfg.DefaultGateway, SourceDefault
	}
	if value == "" {
		value, source = DefaultURL, SourceBuiltin
	}
	if strings.Contains(value, "://") {
		gw, err := sandbox.ParseGateway(value)
		if err != nil {
			return Choice{}, err
		}
		return Choice{URL: value, Gateway: gw, Source: source}, nil
	}
	g, ok := cfg.Gateways[value]
	if !ok {
		return Choice{}, unknown(path, value, source, cfg)
	}
	// load checked the URL.
	gw, _ := sandbox.ParseGateway(g.URL)
	return Choice{Name: value, URL: g.URL, Gateway: gw, Source: source}, nil
}

// load parses and checks the config at path. A missing file is an empty
// config.
func load(path string) (config, error) {
	var cfg config
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	} else if err != nil {
		return cfg, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.Gateways)) {
		if !validName.MatchString(name) {
			return cfg, fmt.Errorf("%s: gateway name %q: use letters, digits, '.', '-' and '_'", path, name)
		}
		g := cfg.Gateways[name]
		if g.URL == "" {
			return cfg, fmt.Errorf("%s: gateway %s: url not set", path, name)
		}
		if _, err := sandbox.ParseGateway(g.URL); err != nil {
			return cfg, fmt.Errorf("%s: gateway %s: %w", path, name, err)
		}
	}
	if d := cfg.DefaultGateway; d != "" {
		if _, ok := cfg.Gateways[d]; !ok {
			return cfg, fmt.Errorf("%s: defaultGateway %q is not a gateway defined there%s", path, d, defined(cfg))
		}
	}
	return cfg, nil
}

// unknown is the error for a gateway name the config doesn't define.
func unknown(path, name, source string, cfg config) error {
	if len(cfg.Gateways) == 0 {
		return fmt.Errorf("unknown gateway %q (from %s): no gateways are defined in %s; give a URL such as %s, or define it there", name, source, path, DefaultURL)
	}
	return fmt.Errorf("unknown gateway %q (from %s)%s", name, source, defined(cfg))
}

// defined lists the config's gateway names, for an error.
func defined(cfg config) string {
	if len(cfg.Gateways) == 0 {
		return "; none are defined"
	}
	return "; defined: " + strings.Join(slices.Sorted(maps.Keys(cfg.Gateways)), ", ")
}

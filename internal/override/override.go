// Package override reads Agent overrides: the user's own settings for an
// agent, kept on the host in kota's config directory in the agent's own
// format, and merged over kota's bundled agent config. Only the keys an
// override's Policy allows can be set; a file with any other key is
// refused, so a key an agent adds in a later version stays refused until
// someone allows it.
package override

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// MaxSize caps the size of an override file kota reads.
const MaxSize = 64 << 10

// schemaKey is always allowed, for editor completion, and dropped.
const schemaKey = "$schema"

// Policy is what an override file may set.
type Policy struct {
	// Allowed are the top-level keys it may set. An allowed object is
	// allowed as a whole.
	Allowed []string
	// AnyKey allows every top-level key, for a file that can only name
	// harmless settings, such as a map of keybindings.
	AnyKey bool
	// Refused gives, for some keys that aren't allowed, why not.
	Refused map[string]string
	// Value, when set, checks each top-level value.
	Value func(v any) error
}

// File is one override file an agent reads.
type File struct {
	// Name is its path under kota's config directory, for example
	// "opencode/tui.json".
	Name   string
	Policy Policy
}

// Path is where the override named name is read from; configHome is
// instructions.ConfigHome.
func Path(configHome, name string) string {
	return filepath.Join(configHome, "kota", filepath.FromSlash(name))
}

// Load reads the override at file. found is false, with no error, when
// there is none. The file must be a regular file of at most MaxSize bytes
// holding one JSON object whose keys p allows; anything else is an error
// naming file.
func Load(file string, p Policy) (over map[string]any, found bool, err error) {
	info, err := os.Stat(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("agent override: %w", err)
	case !info.Mode().IsRegular():
		return nil, false, fmt.Errorf("agent override %s: not a regular file", file)
	case info.Size() > MaxSize:
		return nil, false, fmt.Errorf("agent override %s: larger than %d bytes", file, MaxSize)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, false, fmt.Errorf("agent override: %w", err)
	}
	// Checked again: the file may have grown since the Stat.
	if len(data) > MaxSize {
		return nil, false, fmt.Errorf("agent override %s: larger than %d bytes", file, MaxSize)
	}
	if over, err = decode(data); err != nil {
		return nil, false, fmt.Errorf("agent override %s: %w", file, err)
	}
	delete(over, schemaKey)
	for _, k := range slices.Sorted(maps.Keys(over)) {
		if p.AnyKey || slices.Contains(p.Allowed, k) {
			if p.Value == nil {
				continue
			}
			if err := p.Value(over[k]); err != nil {
				return nil, false, fmt.Errorf("agent override %s: %q: %w", file, k, err)
			}
			continue
		}
		why := ""
		if r := p.Refused[k]; r != "" {
			why = " (" + r + ")"
		}
		return nil, false, fmt.Errorf("agent override %s: %q can't be overridden%s; allowed: %s",
			file, k, why, strings.Join(slices.Sorted(slices.Values(p.Allowed)), ", "))
	}
	return over, true, nil
}

// decode parses data as exactly one JSON object.
func decode(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("invalid JSON: data after the object")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("not a JSON object")
	}
	return obj, nil
}

// Merge returns base with over on top: objects merge key by key, anything
// else in over replaces base's value. base is not changed.
func Merge(base, over map[string]any) map[string]any {
	out := maps.Clone(base)
	if out == nil {
		out = map[string]any{}
	}
	for k, v := range over {
		bo, bok := out[k].(map[string]any)
		oo, ook := v.(map[string]any)
		if bok && ook {
			out[k] = Merge(bo, oo)
		} else {
			out[k] = v
		}
	}
	return out
}

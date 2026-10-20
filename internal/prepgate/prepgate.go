// Package prepgate decides when prep asks before it runs. prep has network
// access, and the agent can write the files that decide what it runs, so
// changes made since the last prep are shown for review first.
//
// After each successful prep a Record of the project is stored on the host,
// outside the sandbox. The next prep compares the project with it.
package prepgate

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"kota/internal/changereport"
	"kota/internal/deps"
	"kota/internal/mask"
)

// Inputs are the files that decide what prep runs, in the syntax of
// changereport.Watched: manifests, lockfiles, package manager config and
// hooks, vendored yarn releases and plugins, patches, and the project
// config.
var Inputs = []string{
	"package.json",
	"package-lock.json",
	"npm-shrinkwrap.json",
	"pnpm-lock.yaml",
	"pnpm-workspace.yaml",
	"yarn.lock",
	".pnpmfile.cjs",
	".pnpmfile.mjs",
	".npmrc",
	".yarnrc",
	".yarnrc.yml",
	".yarn/releases/",
	".yarn/plugins/",
	".yarn/patches/",
	"patches/",
	"go.mod",
	"go.sum",
	"go.work",
	"go.work.sum",
	"kota.yaml",
}

// Record is the state of a project after a prep.
type Record struct {
	// Inputs are the digests of the files matching Inputs.
	Inputs map[string]string `json:"inputs"`
	// All are the digests of every file outside dependency and build
	// directories, which install scripts may run.
	All map[string]string `json:"all"`
	// Dirs are the dependency directories prep installed into. Sessions
	// reuse them when the project's dependencies can't be worked out.
	Dirs []deps.Dir `json:"dirs,omitempty"`
	// Npmrc is set when the project has a root mask.Npmrc, which prep
	// reads. In a saved record it means the user approved that.
	Npmrc bool `json:"npmrc,omitempty"`
}

// Snapshot records project as it is now.
func Snapshot(project string) (Record, error) {
	inputs, err := changereport.SnapshotMatching(project, Inputs)
	if err != nil {
		return Record{}, err
	}
	all, err := changereport.SnapshotAll(project)
	if err != nil {
		return Record{}, err
	}
	_, err = os.Lstat(filepath.Join(project, mask.Npmrc))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Record{}, err
	}
	return Record{Inputs: inputs.Digests(), All: all.Digests(), Npmrc: err == nil}, nil
}

// Load reads the record at file. A missing file returns nil.
func Load(file string) (*Record, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Save writes r to file, readable only by the user.
func (r Record) Save(file string) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// Decision is what changed since the last prep.
type Decision struct {
	// First is set when the project has no record: it was never prepped,
	// or its record was removed.
	First bool
	// Inputs are changed files matching Inputs.
	Inputs []changereport.Change
	// Others are the other changed files. They only count when the install
	// runs project code (hooks), which may run any of them.
	Others []changereport.Change
	// Npmrc is set when the project has a root mask.Npmrc, which prep
	// reads.
	Npmrc bool
	// NewNpmrc is set when prep would read it and the last prep didn't, or
	// wasn't recorded as reading it.
	NewNpmrc bool
}

// Ask reports whether prep must ask before running.
func (d Decision) Ask() bool {
	return d.First || d.NewNpmrc || len(d.Inputs) > 0 || len(d.Others) > 0
}

// Decide compares now with the last record, nil if there is none. hooks
// reports whether the install runs project code.
func Decide(last *Record, now Record, hooks bool) Decision {
	if last == nil {
		return Decision{First: true, Npmrc: now.Npmrc, NewNpmrc: now.Npmrc}
	}
	d := Decision{
		Inputs:   changereport.DiffDigests(last.Inputs, now.Inputs),
		Npmrc:    now.Npmrc,
		NewNpmrc: now.Npmrc && !last.Npmrc,
	}
	if !hooks {
		return d
	}
	for _, c := range changereport.DiffDigests(last.All, now.All) {
		if _, input := now.Inputs[c.Path]; input {
			continue
		}
		if _, input := last.Inputs[c.Path]; input {
			continue
		}
		d.Others = append(d.Others, c)
	}
	return d
}

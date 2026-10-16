// Package hostdata is where kota keeps its state on the host: under
// kota/ in the user cache dir, outside every project and Sandbox. The
// user cache dir lives under $HOME, so Docker VMs share it, which session
// directories need for their bind mounts.
//
// Docker volumes are listed by package sandbox; everything else kota
// keeps for a project is found through ProjectData, so clean can't miss a
// kind of state.
package hostdata

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"kota/internal/engine"
	"kota/internal/sandbox"
)

// Dirs is kota's directory on the host.
type Dirs struct {
	Root string
}

// Default is kota's directory in the user cache dir. There is no
// fallback: a record kept in a shared directory such as /tmp could be
// planted by another user.
func Default() (Dirs, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return Dirs{}, fmt.Errorf("find kota's data directory: %w", err)
	}
	return Dirs{Root: filepath.Join(cache, "kota")}, nil
}

// Engine holds the Engine base disk and each project's engine data.
func (d Dirs) Engine() engine.Layout { return engine.Layout{Root: filepath.Join(d.Root, "engine")} }

// Registry holds the session registry's records and locks.
func (d Dirs) Registry() string { return filepath.Join(d.Root, "registry") }

// PrepRecord is where the project's state after its last prep is kept.
func (d Dirs) PrepRecord(project string) string {
	return filepath.Join(d.Root, "prep", sandbox.ProjectName(project)+".json")
}

// Sessions holds the session directories.
func (d Dirs) Sessions() string { return filepath.Join(d.Root, "sessions") }

// NewSessionDir creates a directory for one session of project, named
// <project name>-<pid>-<random digits>, as SessionDirs expects.
func (d Dirs) NewSessionDir(project string) (string, error) {
	if err := os.MkdirAll(d.Sessions(), 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(d.Sessions(), sandbox.ProjectName(project)+"-"+strconv.Itoa(os.Getpid())+"-")
}

// SessionDirs lists project's session directories. Under the project's
// lease none of them belongs to a running session: they were left by
// sessions that didn't end cleanly.
func (d Dirs) SessionDirs(project string) ([]string, error) {
	entries, err := os.ReadDir(d.Sessions())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("list session directories: %w", err)
	}
	// The exact pattern keeps out a project whose name extends this one's.
	own := regexp.MustCompile(`^` + regexp.QuoteMeta(sandbox.ProjectName(project)) + `-[0-9]+-[0-9]+$`)
	var dirs []string
	for _, e := range entries {
		if own.MatchString(e.Name()) {
			dirs = append(dirs, filepath.Join(d.Sessions(), e.Name()))
		}
	}
	return dirs, nil
}

// ProjectData is what kota keeps on the host for one project, apart
// from its Docker volumes. The shared Engine base disk isn't part of it.
type ProjectData struct {
	// PrepRecord is the project's prep record, or "" when there is none.
	PrepRecord string
	// EngineDir is the project's engine directory, with the image archives
	// prep downloaded, or "" when there is none.
	EngineDir string
	// Images are the archived images its record lists, if it can be read.
	Images []string
	// Sessions are session directories left by sessions that didn't end
	// cleanly. They may still hold that session's TLS certificates.
	Sessions []string
}

// Empty reports whether there is nothing to remove.
func (p ProjectData) Empty() bool {
	return p.PrepRecord == "" && p.EngineDir == "" && len(p.Sessions) == 0
}

// Paths are the files and directories to remove, in that order: the prep
// record goes last, so a failed removal keeps the next prep's confirmation.
func (p ProjectData) Paths() []string {
	var paths []string
	if p.EngineDir != "" {
		paths = append(paths, p.EngineDir)
	}
	paths = append(paths, p.Sessions...)
	if p.PrepRecord != "" {
		paths = append(paths, p.PrepRecord)
	}
	return paths
}

// ProjectData lists what kota keeps for project. It doesn't parse
// kota.yaml, so it works when that is broken or gone. Call it under
// the project's lease.
func (d Dirs) ProjectData(project string) (ProjectData, error) {
	var p ProjectData
	record := d.PrepRecord(project)
	if _, err := os.Lstat(record); err == nil {
		p.PrepRecord = record
	} else if !errors.Is(err, fs.ErrNotExist) {
		return p, fmt.Errorf("inspect prep record: %w", err)
	}
	layout := d.Engine()
	dir := layout.ProjectDir(project)
	if _, err := os.Lstat(dir); err == nil {
		p.EngineDir = dir
		if rec, err := engine.LoadRecord(layout.ArchiveDir(project)); err == nil && rec != nil {
			for _, a := range rec.Archives {
				p.Images = append(p.Images, a.Ref)
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return p, fmt.Errorf("inspect engine data: %w", err)
	}
	var err error
	if p.Sessions, err = d.SessionDirs(project); err != nil {
		return p, err
	}
	return p, nil
}

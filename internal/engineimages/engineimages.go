// Package engineimages is a project's cache of Engine image archives, which
// prep fills and every Session loads into the Engine VM.
//
// Plan works out what prep does with the declared images. Before the user
// confirms, it looks up only the images the last prep recorded, so a
// reference an agent wrote into kota.yaml reaches the network only
// after the user approved it. Fetch then downloads only new and moved
// images, by digest, and replaces the cache only once every download
// succeeded.
//
// Lookups and downloads run crane through Run, in a throwaway container
// that has only an empty work directory mounted: no project files, so no
// project code.
package engineimages

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"kota/internal/engine"
)

// Cache is one project's archive directory.
type Cache struct {
	// Dir is the project's archive directory (engine.Layout.ArchiveDir).
	// Work directories are made next to it.
	Dir string
	// Platform is the platform images are downloaded for, e.g. linux/arm64.
	Platform string
	// Run runs script in a throwaway container with network access and
	// only work mounted, as its working directory.
	Run func(work, script string) error
	// Out receives progress.
	Out io.Writer
}

// Plan works out what prep does with declared, the images of the project's
// engine section (nil without one). It returns nil when the project
// declares no images and none were prepped.
func (c Cache) Plan(declared []string) (*engine.ImagePlan, error) {
	last, err := engine.LoadRecord(c.Dir)
	if err != nil {
		return nil, err
	}
	if declared == nil && last == nil {
		return nil, nil
	}
	var resolved map[string]string
	if last != nil && len(last.Archives) > 0 {
		var recorded []string
		for _, a := range last.Archives {
			recorded = append(recorded, a.Ref)
		}
		fmt.Fprintf(c.Out, "looking up the %d engine image tag(s) of the last prep\n", len(recorded))
		if resolved, err = c.resolve(recorded); err != nil {
			return nil, err
		}
	}
	have := func(a engine.Archive) bool { return regularFile(filepath.Join(c.Dir, a.File)) == nil }
	p := engine.Plan(declared, last, resolved, have)
	return &p, nil
}

// resolve looks up the digests images' tags point to now.
func (c Cache) resolve(images []string) (map[string]string, error) {
	work := c.Dir + ".resolve"
	if err := os.RemoveAll(work); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(work, 0o700); err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	if err := c.Run(work, engine.ResolveScript(images)); err != nil {
		return nil, scriptErr(work, "look up", images, err)
	}
	return readDigests(work, images)
}

// Fetch carries out p. Only p.Downloads are downloaded; the archives p
// keeps are linked into a new directory, which replaces the previous one
// only after every download succeeded.
func (c Cache) Fetch(p *engine.ImagePlan) error {
	if len(p.Downloads) == 0 && !p.Changed() {
		fmt.Fprintf(c.Out, "engine images up to date: %d, as at the last prep\n", len(p.Keep))
		return nil
	}
	if len(p.Downloads) > 0 && c.Platform == "" {
		return errors.New("the Engine VM doesn't support this host, so its images can't be downloaded")
	}
	staging := c.Dir + ".new"
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	digests := map[string]string{}
	if len(p.Downloads) > 0 {
		var refs []string
		for _, a := range p.Downloads {
			refs = append(refs, a.Ref)
		}
		fmt.Fprintf(c.Out, "downloading %d engine image(s) for %s (no project code runs); %d unchanged\n", len(refs), c.Platform, len(p.Keep))
		if err := c.Run(staging, engine.FetchScript(p.Downloads, c.Platform)); err != nil {
			return scriptErr(staging, "download", refs, err)
		}
		var err error
		if digests, err = readDigests(staging, refs); err != nil {
			return err
		}
		for _, a := range p.Downloads {
			if err := regularFile(filepath.Join(staging, a.File)); err != nil {
				return fmt.Errorf("download engine image %s: %w", a.Ref, err)
			}
		}
		if err := os.Remove(filepath.Join(staging, engine.DigestsFile)); err != nil {
			return err
		}
	}
	for _, a := range p.Keep {
		if err := os.Link(filepath.Join(c.Dir, a.File), filepath.Join(staging, a.File)); err != nil {
			return fmt.Errorf("keep engine image %s: %w", a.Ref, err)
		}
	}
	rec, err := p.Record(digests)
	if err != nil {
		return err
	}
	if len(rec.Archives) == 0 {
		return os.RemoveAll(c.Dir)
	}
	if err := rec.Save(staging); err != nil {
		return err
	}
	if err := os.RemoveAll(c.Dir); err != nil {
		return err
	}
	return os.Rename(staging, c.Dir)
}

// readDigests reads the digests a script wrote into dir for images. The
// directory was written by a container: only regular files are taken, so a
// symlink can't point the host at its own files.
func readDigests(dir string, images []string) (map[string]string, error) {
	file := filepath.Join(dir, engine.DigestsFile)
	if err := regularFile(file); err != nil {
		return nil, fmt.Errorf("engine image digests: %w", err)
	}
	content, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return engine.ParseDigests(images, string(content))
}

// scriptErr is err from a failed script in dir, naming the image it
// stopped on when that is one of images.
func scriptErr(dir, what string, images []string, err error) error {
	file := filepath.Join(dir, engine.FailedFile)
	if regularFile(file) == nil {
		if content, rerr := os.ReadFile(file); rerr == nil {
			if ref := strings.TrimSpace(string(content)); slices.Contains(images, ref) {
				return fmt.Errorf("%s engine image %s: crane failed (its output is above); check the reference in %s: %w", what, ref, engine.ConfigFile, err)
			}
		}
	}
	return fmt.Errorf("%s engine images: %w", what, err)
}

// regularFile returns an error unless file is a non-empty regular file, not
// a symlink.
func regularFile(file string) error {
	info, err := os.Lstat(file)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("%s is not a non-empty regular file", file)
	}
	return nil
}

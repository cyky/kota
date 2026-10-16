// Package projectimage handles a project's own image, built from File at
// the project root on top of the base image.
//
// The image is labeled with the digest of the Dockerfile it was built from
// and the ID of the base image it was built on, so a session can tell that
// it is stale.
package projectimage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	// File is the project's Dockerfile at the project root.
	File = "kota.Dockerfile"

	// LabelDockerfile holds the sha256 of the Dockerfile an image was built
	// from.
	LabelDockerfile = "kota.dockerfile-sha256"
	// LabelBase holds the ID of the base image when the image was built.
	LabelBase = "kota.base-id"
)

// Tag returns the image tag of a project directory. It is stable for a
// path and differs across paths.
func Tag(projectDir string) string {
	sum := sha256.Sum256([]byte(projectDir))
	return fmt.Sprintf("kota-project-%s:latest", hex.EncodeToString(sum[:])[:32])
}

// Read returns the project's Dockerfile, or nil when there is none. A file
// with only whitespace counts as none: it is the placeholder a running
// session mounts where the file is missing. A symlink is refused, so the
// file can't be a host file outside the project.
func Read(projectDir string) ([]byte, error) {
	p := filepath.Join(projectDir, File)
	info, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", p)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	return data, nil
}

// Digest is the value of LabelDockerfile for a Dockerfile.
func Digest(dockerfile []byte) string {
	sum := sha256.Sum256(dockerfile)
	return hex.EncodeToString(sum[:])
}

// Labels returns the labels for an image built from dockerfile on the base
// image with ID baseID.
func Labels(dockerfile []byte, baseID string) map[string]string {
	return map[string]string{LabelDockerfile: Digest(dockerfile), LabelBase: baseID}
}

// Image is what Docker has under a project's tag.
type Image struct {
	Exists bool
	Labels map[string]string
}

// Choice is the image a session of a project uses.
type Choice struct {
	Image string
	// Warning, when set, says the project image is missing or stale and how
	// to fix it.
	Warning string
}

// Choose picks the image for a project: its own image when it has a
// Dockerfile and the image exists, else base. dockerfile is from Read,
// built is what Docker has under Tag, and baseID is the base image's
// current ID.
func Choose(projectDir, base string, dockerfile []byte, built Image, baseID string) Choice {
	if dockerfile == nil {
		return Choice{Image: base}
	}
	fix := fmt.Sprintf("run 'kota build %s'", projectDir)
	if !built.Exists {
		return Choice{Image: base, Warning: fmt.Sprintf("%s has no image yet, so this session uses the base image; %s", File, fix)}
	}
	c := Choice{Image: Tag(projectDir)}
	switch {
	case built.Labels[LabelDockerfile] != Digest(dockerfile):
		c.Warning = fmt.Sprintf("%s changed since its image was built, so this session uses the old image; %s", File, fix)
	case built.Labels[LabelBase] != baseID:
		c.Warning = fmt.Sprintf("the base image changed since the project image was built on it, so this session uses the old image; %s", fix)
	}
	return c
}

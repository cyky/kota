package projectimage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const base = "kota-base:latest"

func TestTag(t *testing.T) {
	a, b := Tag("/Users/me/Projects/a"), Tag("/Users/me/Projects/b")
	if a == b {
		t.Errorf("two projects share the tag %s", a)
	}
	if a != Tag("/Users/me/Projects/a") {
		t.Error("tag is not stable")
	}
	if !strings.HasPrefix(a, "kota-project-") || !strings.HasSuffix(a, ":latest") {
		t.Errorf("tag = %s", a)
	}
}

func TestChoose(t *testing.T) {
	const project = "/Users/me/Projects/a"
	df := []byte("FROM kota-base:latest\nRUN apt-get install -y tree\n")
	current := Image{Exists: true, Labels: Labels(df, "sha256:base1")}

	cases := []struct {
		name       string
		dockerfile []byte
		built      Image
		baseID     string
		image      string
		warning    string
	}{
		{name: "no Dockerfile", built: current, baseID: "sha256:base1", image: base},
		{name: "current", dockerfile: df, built: current, baseID: "sha256:base1", image: Tag(project)},
		{name: "missing", dockerfile: df, baseID: "sha256:base1", image: base, warning: "has no image yet"},
		{name: "Dockerfile changed", dockerfile: append(df, "RUN true\n"...), built: current, baseID: "sha256:base1", image: Tag(project), warning: "changed since its image was built"},
		{name: "base rebuilt", dockerfile: df, built: current, baseID: "sha256:base2", image: Tag(project), warning: "base image changed"},
		{name: "unlabeled", dockerfile: df, built: Image{Exists: true}, baseID: "sha256:base1", image: Tag(project), warning: "changed since"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Choose(project, base, c.dockerfile, c.built, c.baseID)
			if got.Image != c.image {
				t.Errorf("image = %s, want %s", got.Image, c.image)
			}
			if c.warning == "" && got.Warning != "" {
				t.Errorf("unexpected warning %q", got.Warning)
			}
			if c.warning != "" && (!strings.Contains(got.Warning, c.warning) || !strings.Contains(got.Warning, "kota build "+project)) {
				t.Errorf("warning = %q, want %q and the build command", got.Warning, c.warning)
			}
		})
	}
}

func TestRead(t *testing.T) {
	dir := t.TempDir()
	if df, err := Read(dir); df != nil || err != nil {
		t.Errorf("missing file: %q, %v", df, err)
	}
	p := filepath.Join(dir, File)
	// A running session's placeholder is empty.
	if err := os.WriteFile(p, []byte(" \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if df, err := Read(dir); df != nil || err != nil {
		t.Errorf("empty file: %q, %v", df, err)
	}
	if err := os.WriteFile(p, []byte("FROM x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if df, err := Read(dir); string(df) != "FROM x\n" || err != nil {
		t.Errorf("file: %q, %v", df, err)
	}

	other := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(other, []byte("FROM x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Remove(p)
	if err := os.Symlink(other, p); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); err == nil {
		t.Error("symlink was read")
	}
}

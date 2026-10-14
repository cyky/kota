//go:build integration

// Project image tests build an image from a localllm.Dockerfile, which
// installs an apt package, so they need network access as well as Docker.
package check

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"localllm/internal/docker"
	"localllm/internal/mask"
	"localllm/internal/probe"
	"localllm/internal/projectimage"
	"localllm/internal/sandbox"
)

// buildProjectImage builds project's Dockerfile the way 'localllm build
// dir' does and removes the image after the test.
func buildProjectImage(t *testing.T, project string) string {
	t.Helper()
	dockerfile, err := projectimage.Read(project)
	if err != nil || dockerfile == nil {
		t.Fatalf("read Dockerfile: %q, %v", dockerfile, err)
	}
	baseID, _, ok, err := docker.InspectImage(testAgentImage)
	if err != nil || !ok {
		t.Fatalf("inspect base: %v %v", ok, err)
	}
	tag := projectimage.Tag(project)
	t.Cleanup(func() { exec.Command("docker", "image", "rm", "-f", tag).Run() })
	if err := docker.BuildDockerfile(tag, dockerfile, projectimage.Labels(dockerfile, baseID)); err != nil {
		t.Fatalf("build: %v", err)
	}
	return tag
}

// TestProjectImagePackageInRun builds a project image with an extra apt
// package and checks a contained session on it has the package, the
// Dockerfile is read-only, and the image counts as current.
func TestProjectImagePackageInRun(t *testing.T) {
	requireDocker(t)
	project := writeProject(t, map[string]string{
		projectimage.File: "FROM " + testAgentImage + "\n" +
			"RUN apt-get update && apt-get install -y --no-install-recommends tree && rm -rf /var/lib/apt/lists/*\n",
	})
	if _, err := exec.Command("docker", "run", "--rm", "--entrypoint", "sh", testAgentImage, "-c", "command -v tree").Output(); err == nil {
		t.Fatal("the base image already has tree; pick another package")
	}
	tag := buildProjectImage(t, project)

	dockerfile, _ := projectimage.Read(project)
	baseID, _, _, _ := docker.InspectImage(testAgentImage)
	_, labels, exists, err := docker.InspectImage(tag)
	if err != nil {
		t.Fatal(err)
	}
	choice := projectimage.Choose(project, testAgentImage, dockerfile, projectimage.Image{Exists: exists, Labels: labels}, baseID)
	if choice.Image != tag || choice.Warning != "" {
		t.Errorf("choice after build = %+v", choice)
	}
	// Editing the Dockerfile makes the image stale.
	edited := append(dockerfile, "RUN true\n"...)
	if c := projectimage.Choose(project, testAgentImage, edited, projectimage.Image{Exists: exists, Labels: labels}, baseID); c.Warning == "" {
		t.Error("no warning for an edited Dockerfile")
	}

	layout, err := mask.Plan(project, mask.DefaultRules)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { layout.Cleanup(project) })
	sessionDir := t.TempDir()
	pinned, ro, err := layout.Bind(project, sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := layout.Prepare(project); err != nil {
		t.Fatal(err)
	}
	var m Mounts
	m.Pinned, m.ReadOnly, m.Masked = layout.Targets()
	in := sandbox.Inputs{ProjectDir: project, Pinned: pinned, ReadOnly: ro}
	s, gw := startInputs(t, in, func(spec *sandbox.Compose) {
		agent := spec.Services[sandbox.AgentService]
		agent.Image = tag
		spec.Services[sandbox.AgentService] = agent
	})

	out, err := s.Capture("sh", "-c", "tree --version")
	if err != nil || !strings.Contains(out, "tree v") {
		t.Errorf("tree in run: %q, %v", out, err)
	}
	results, err := Run(s, gw, m)
	if err != nil {
		t.Fatal(err)
	}
	requirePass(t, results)
	var dockerfileProbed bool
	for _, r := range results {
		if strings.Contains(r.Name, projectimage.File) {
			dockerfileProbed = true
		}
	}
	if !dockerfileProbed {
		t.Errorf("no probe covers %s: %v", projectimage.File, names(results))
	}
	if failed := probe.Failed(results); len(failed) > 0 {
		t.Errorf("failed probes: %v", names(failed))
	}
}

// TestProjectImageHasNoBuildContext checks a Dockerfile can't copy project
// files, such as masked secrets, into the image.
func TestProjectImageHasNoBuildContext(t *testing.T) {
	requireDocker(t)
	project := projectDir(t)
	if err := os.WriteFile(filepath.Join(project, ".env"), []byte("SECRET=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tag := projectimage.Tag(project)
	t.Cleanup(func() { exec.Command("docker", "image", "rm", "-f", tag).Run() })
	dockerfile := []byte("FROM " + testAgentImage + "\nCOPY .env /secret\n")
	if err := docker.BuildDockerfile(tag, dockerfile, nil); err == nil {
		t.Error("COPY .env succeeded; the build context is not empty")
	}
}

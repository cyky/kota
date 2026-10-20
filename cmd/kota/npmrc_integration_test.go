//go:build integration

package main

import (
	"os"
	"path/filepath"
	"testing"

	"kota/internal/engineimages"
	"kota/internal/sandbox"
)

// TestPrepReadsNpmrc runs both prep phases with checks in place of the
// installs: each sees the project-root .npmrc as it is and can't write it,
// a nested one reads as empty, and no host npm config or token gets in.
func TestPrepReadsNpmrc(t *testing.T) {
	requireBaseImage(t)
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(cache, "kota-test-")
	if err != nil {
		t.Fatal(err)
	}
	root, _ = filepath.EvalSymlinks(root)
	t.Cleanup(func() { os.RemoveAll(root) })
	t.Setenv("KOTA_ALLOWED_ROOT", root)
	t.Setenv("NPM_TOKEN", "host-token")
	project := filepath.Join(root, "app")
	for rel, content := range map[string]string{
		"package.json":      "{}",
		"pnpm-lock.yaml":    "",
		".npmrc":            "//registry.example.com/:_authToken=secret\n",
		"packages/a/.npmrc": "nested\n",
	} {
		p := filepath.Join(project, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hostGit(t, project, "init", "-q")

	sa := sessionArgs{dir: project}
	lease, err := lockProject(sa, "prep")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseProject(lease)
	depPlan, err := detectDeps(project)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := prepareSession(sa, sandbox.ModePrep, lease.paths, lease.paths.Project, depPlan)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.cleanup()
	const check = `set -e
test "$(cat /workspace/.npmrc)" = "//registry.example.com/:_authToken=secret"
if (echo x >> /workspace/.npmrc) 2>/dev/null; then echo "kota-test: .npmrc is writable"; exit 1; fi
test ! -s /workspace/packages/a/.npmrc
test -z "${NPM_TOKEN:-}"
test ! -e "$HOME/.npmrc"
`
	caches := renameCaches(t)
	for _, spec := range []*sandbox.Compose{&sess.spec, &sess.fetchSpec} {
		caches(spec)
		svc := spec.Services[sandbox.AgentService]
		svc.Command = []string{"sh", "-c", check}
		spec.Services[sandbox.AgentService] = svc
	}
	if err := runPrep(sess, engineimages.Cache{}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(project, ".npmrc"))
	if err != nil || string(got) != "//registry.example.com/:_authToken=secret\n" {
		t.Errorf("host .npmrc = %q, %v", got, err)
	}
}

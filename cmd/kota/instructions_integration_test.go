//go:build integration

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kota/internal/docker"
	"kota/internal/sandbox"
)

// TestInstructionsInShell opens a shell Session for each agent, with an
// override in a test-only XDG_CONFIG_HOME, and checks the note at the
// agent's path holds the override and the Sandbox facts, and can't be
// written, replaced or removed.
func TestInstructionsInShell(t *testing.T) {
	requireBaseImage(t)
	// HOME stays, since the docker CLI needs it.
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(config, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if err := os.MkdirAll(filepath.Join(config, "kota"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "kota", "instructions.md"), []byte("# Override\n\nOVERRIDE-MARKER\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	project := filepath.Join(root, "app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	hostGit(t, project, "init", "-q")

	for _, agent := range []string{"opencode", "pi"} {
		t.Run(agent, func(t *testing.T) {
			sa := sessionArgs{agent: agent, gatewayURL: stubModelGateway(t), dir: project, shell: true}
			var out string
			withSession(t, sa, func(*sandbox.Compose) {}, func(sess *session, ds *docker.Session) {
				note := sess.bridge.NotePath
				out, err = ds.Capture("bash", "-c", `exec 2>&1
note=$1
cat "$note"
echo x >> "$note" && echo WROTE
rm -f "$note" && echo REMOVED
mv "$note" "$note.old" && echo MOVED
true`, "bash", note)
				if err != nil {
					t.Fatalf("%v\n%s", err, out)
				}
			})
			t.Logf("inside:\n%s", out)
			if !strings.HasPrefix(out, "# Override\n\nOVERRIDE-MARKER\n\n# This sandbox\n") {
				t.Errorf("note doesn't start with the override, then the Sandbox facts:\n%s", out)
			}
			if strings.Contains(out, "# Working on tasks") {
				t.Error("the built-in style is in the note despite the override")
			}
			for _, bad := range []string{"WROTE", "REMOVED", "MOVED"} {
				if strings.Contains(out, bad) {
					t.Errorf("the agent could change its note: %s", bad)
				}
			}
		})
	}
}

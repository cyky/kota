//go:build integration

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"kota/internal/agent"
	"kota/internal/changereport"
	"kota/internal/docker"
	"kota/internal/sandbox"
)

func requireBaseImage(t *testing.T) {
	t.Helper()
	if exec.Command("docker", "image", "inspect", baseImage).Run() != nil {
		t.Skip("docker or " + baseImage + " not available; run 'kota build'")
	}
}

func hostGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestAgentCommit has the agent commit everything with git add -A in a
// shell session, and checks what the commit holds and what the change
// report says about it.
func TestAgentCommit(t *testing.T) {
	requireBaseImage(t)
	// The user's global git config and excludes are test-only; HOME stays,
	// since the docker CLI needs it.
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(config, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if err := os.MkdirAll(filepath.Join(config, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "git", "ignore"), []byte(".claude/settings.local.json\n"), 0o644); err != nil {
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
	for rel, content := range map[string]string{
		".npmrc":                      "//registry.example.com/:_authToken=secret\n",
		".github/workflows/ci.yml":    "on: push\n",
		"README.md":                   "hi\n",
		".env":                        "TOKEN=secret\n",
		".claude/settings.local.json": "{}\n",
	} {
		p := filepath.Join(project, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hostGit(t, project, "init", "-q", "-b", "main")
	hostGit(t, project, "config", "user.name", "t")
	hostGit(t, project, "config", "user.email", "t@example.com")
	hostGit(t, project, "add", ".npmrc", ".github", "README.md")
	hostGit(t, project, "commit", "-q", "-m", "start")

	var report bytes.Buffer
	var result *changereport.Result
	var inside string
	sa := sessionArgs{agent: agent.Default, gatewayURL: stubModelGateway(t), dir: project, shell: true}
	withSession(t, sa, func(*sandbox.Compose) {}, func(sess *session, ds *docker.Session) {
		// Started here, after the session wrote its placeholders, as in
		// cmdOffline.
		changes, err := changereport.Start(project, sess.layout, offlineGit, changeOptions(false))
		if err != nil {
			t.Fatal(err)
		}
		inside, err = ds.Capture("bash", "-c", `exec 2>&1
set -e
cd /workspace
git check-ignore -q .claude/settings.local.json && echo GLOBAL-IGNORED-INSIDE
cat /etc/kota/git-excludes
echo '# changed' >> .github/workflows/ci.yml
git add -A
git commit -q -m agent
git show HEAD~:.github/workflows/ci.yml > .github/workflows/ci.yml
git switch -q -c wip
git commit -q --allow-empty -m 'left on wip'
git switch -q main
echo DONE`)
		if err != nil {
			t.Fatalf("%v\n%s", err, inside)
		}
		// Before withSession's cleanup, as in cmdOffline.
		result = changes.Finish()
	})
	result.Report(&report)
	t.Logf("inside:\n%s\nreport:\n%s", inside, report.String())

	if !strings.Contains(inside, "DONE") {
		t.Fatalf("script failed:\n%s", inside)
	}
	if strings.Contains(inside, "GLOBAL-IGNORED-INSIDE") || strings.Contains(inside, "settings.local") {
		t.Error("the host's global excludes reached the sandbox")
	}

	files := strings.Split(hostGit(t, project, "show", "--name-status", "--format=", "main"), "\n")
	slices.Sort(files)
	want := []string{"A\t.claude/settings.local.json", "M\t.github/workflows/ci.yml", "M\t.npmrc"}
	if !slices.Equal(files, want) {
		t.Errorf("agent commit holds %q, want %q (no placeholders, no untracked masks)", files, want)
	}

	out := report.String()
	for _, want := range []string{
		"new branch wip",
		"2 new commit(s):",
		".npmrc  masked secret, shown empty in the sandbox: emptied in ",
		"git add -- .npmrc",
		".claude/settings.local.json  ignored by your global git excludes: added in ",
		".github/workflows/ci.yml  can run code on the host, change not in the working tree: modified in ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q", want)
		}
	}
	if strings.Contains(out, "modified  .github/workflows/ci.yml") {
		t.Error("the working tree report flagged ci.yml, whose change is only in the commit")
	}
}

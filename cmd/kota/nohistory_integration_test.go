//go:build integration

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"kota/internal/agent"
	"kota/internal/changereport"
	"kota/internal/docker"
	"kota/internal/hostdata"
	"kota/internal/nohistory"
	"kota/internal/sandbox"
)

// TestNoHistorySession runs a shell session with --no-history on a project
// whose history holds a secret, and checks what the session can see and
// that the copy is kept with the agent's commit, apart from the original.
func TestNoHistorySession(t *testing.T) {
	requireBaseImage(t)
	t.Setenv("KOTA_GATEWAY_KEY", "")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(config, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

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
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(project, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "v1\n")
	write("config/old-secret.yml", "password: HISTORY-SECRET\n")
	write("config/token.txt", "TRACKED-TOKEN\n")
	hostGit(t, project, "init", "-q", "-b", "main")
	hostGit(t, project, "config", "user.name", "t")
	hostGit(t, project, "config", "user.email", "t@example.com")
	hostGit(t, project, "add", ".")
	hostGit(t, project, "commit", "-q", "-m", "start")
	hostGit(t, project, "rm", "-q", "config/old-secret.yml")
	write("README.md", "v2\n")
	hostGit(t, project, "commit", "-q", "-am", "drop the secret")
	hostGit(t, project, "remote", "add", "origin", "https://example.com/app.git")
	head := hostGit(t, project, "rev-parse", "HEAD")
	// Untracked, so not in the commit: it must still mask the tracked token
	// in the copy.
	write(".kotaignore", "config/token.txt\n")

	gateway := stubModelGateway(t)
	t.Cleanup(func() {
		if available, err := docker.ListVolumes(); err == nil {
			docker.RemoveVolumes(sandbox.ProjectVolumes(project, available)...)
		}
	})

	// The agent commits; nothing else is left in the copy.
	run := noHistorySession(t, project, gateway, `
echo "COMMITS=$(git rev-list --all --count)"
echo "REMOTES=$(git remote | wc -l)"
git log -p --all | grep -v TRACKED-TOKEN
echo "TOKEN=$(cat config/token.txt)"
cat .kotaignore
echo v3 > README.md
echo 'all:' > Makefile
git add README.md Makefile
git commit -q -m agent && echo COMMITTED`)
	if !strings.Contains(run.inside, "COMMITTED") {
		t.Fatalf("script failed:\n%s", run.inside)
	}
	// The tracked token is in HEAD, which git shows (kota warns about it),
	// but the file reads as empty.
	for _, want := range []string{"COMMITS=1\n", "REMOTES=0\n", "TOKEN=\n", "config/token.txt\n"} {
		if !strings.Contains(run.inside, want) {
			t.Errorf("inside lacks %q", want)
		}
	}
	for _, leak := range []string{"HISTORY-SECRET", "TRACKED-TOKEN", "example.com/app.git", "password"} {
		if strings.Contains(run.inside, leak) {
			t.Errorf("the session saw %q", leak)
		}
	}
	if want := []string{sandbox.StateVolume(project, agent.Default+sandbox.NoHistoryState)}; !slices.Equal(run.states, want) {
		t.Errorf("state volumes = %q, want %q", run.states, want)
	}
	branch := "kota/shell-" + run.start.Format(hostdata.TimeFormat)
	if got := hostGit(t, project, "rev-parse", branch+"~1"); got != head {
		t.Errorf("the returned branch builds on %s, want %s", got, head)
	}
	if got := hostGit(t, project, "show", branch+":README.md"); got != "v3" {
		t.Errorf("README.md on the branch = %q", got)
	}
	if got := hostGit(t, project, "rev-parse", "HEAD"); got != head {
		t.Errorf("the original's HEAD moved to %s", got)
	}
	if got, _ := os.ReadFile(filepath.Join(project, "README.md")); string(got) != "v2\n" {
		t.Errorf("the original's README = %q", got)
	}
	for _, want := range []string{
		"returned branch " + branch + " (1 commit(s)",
		"Makefile  can run code on the host",
		"the copy was removed",
	} {
		if !strings.Contains(run.report, want) {
			t.Errorf("report lacks %q", want)
		}
	}
	if _, err := os.Stat(run.copy); !os.IsNotExist(err) {
		t.Errorf("copy %s still there: %v", run.copy, err)
	}

	// Work left behind: an uncommitted file and another branch keep the copy.
	run = noHistorySession(t, project, gateway, `
git switch -q -c wip && git commit -q --allow-empty -m wip && git switch -q -
echo note > NOTES.txt
git commit -q --allow-empty -m 'second session' && echo COMMITTED`)
	if !strings.Contains(run.inside, "COMMITTED") {
		t.Fatalf("script failed:\n%s", run.inside)
	}
	for _, want := range []string{
		"returned branch kota/shell-" + run.start.Format(hostdata.TimeFormat) + " (1 commit(s)",
		"not brought back: branch wip",
		"copy kept at " + run.copy + " (uncommitted changes; branches or tags not brought back)",
		"?? NOTES.txt",
	} {
		if !strings.Contains(run.report, want) {
			t.Errorf("report lacks %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(run.copy, "NOTES.txt")); err != nil {
		t.Errorf("kept copy lacks the uncommitted file: %v", err)
	}
}

// noHistoryRun is what noHistorySession saw.
type noHistoryRun struct {
	inside, report, copy string
	start                time.Time
	states               []string
}

// noHistorySession runs script in /workspace of a shell --no-history
// session of project, then the return, as cmdOffline does.
func noHistorySession(t *testing.T, project, gateway, script string) noHistoryRun {
	t.Helper()
	var run noHistoryRun
	var report bytes.Buffer
	sa := sessionArgs{agent: agent.Default, gatewayURL: gateway, dir: project, shell: true, noHistory: true}
	withSession(t, sa, func(*sandbox.Compose) {}, func(sess *session, ds *docker.Session) {
		run.copy = sess.project
		t.Cleanup(func() { os.RemoveAll(run.copy) })
		var err error
		stamp := filepath.Base(run.copy)
		if run.start, err = time.ParseInLocation(hostdata.TimeFormat, stamp[len(stamp)-len(hostdata.TimeFormat):], time.Local); err != nil {
			t.Fatal(err)
		}
		for _, v := range sess.spec.Volumes {
			if strings.HasPrefix(v.Name, "kota-state-") {
				run.states = append(run.states, v.Name)
			}
		}
		changes, err := changereport.Start(run.copy, sess.layout, offlineGit, changeOptions(true))
		if err != nil {
			t.Fatal(err)
		}
		if run.inside, err = ds.Capture("bash", "-c", "exec 2>&1\ncd /workspace\n"+script); err != nil {
			t.Fatalf("%v\n%s", err, run.inside)
		}
		result := changes.Finish()
		// As in cmdOffline.
		sess.unlock()
		ret := nohistory.Return(sess.project, sess.key, result, returnName(sa, agent.Default), run.start,
			offlineGit, filepath.Join(sess.sessionDir, "git-excludes"), sess.sessionDir, sess.copied)
		result.Report(&report)
		ret.Report(&report)
	})
	run.report = report.String()
	t.Logf("inside:\n%s\nreport:\n%s", run.inside, run.report)
	return run
}

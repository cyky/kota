package hostgit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// isolate gives git a fake HOME and global config, with user.name Host.
func isolate(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	global := filepath.Join(home, "gitconfig")
	if err := os.WriteFile(global, []byte("[user]\n\tname = Host\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, v := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_CEILING_DIRECTORIES"} {
		t.Setenv(v, "")
		os.Unsetenv(v)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// plant writes what an agent could leave in project in one session: bare
// repository files at the project root, whose config sets a worktree, an
// fsmonitor that writes marker, and its own identity and hooks path; and,
// when the project has a .git directory, a broken .git/HEAD, which makes
// git's discovery fall back to the root. It returns the marker's path.
func plant(t *testing.T, project string) string {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "fsmonitor-ran")
	write(t, filepath.Join(project, "fsm.sh"), "#!/bin/sh\ntouch "+marker+"\n", 0o755)
	if _, err := os.Stat(filepath.Join(project, ".git")); err == nil {
		write(t, filepath.Join(project, ".git", "HEAD"), "garbage\n", 0o644)
	}
	write(t, filepath.Join(project, "HEAD"), "ref: refs/heads/main\n", 0o644)
	for _, dir := range []string{"objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(project, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(project, "config"), "[core]\n\trepositoryformatversion = 0\n\tbare = false\n\tworktree = "+project+
		"\n\tfsmonitor = "+filepath.Join(project, "fsm.sh")+"\n\thooksPath = /tmp/elsewhere\n[user]\n\tname = AGENT\n", 0o644)
	// An index, so ls-files has something to ask the fsmonitor about.
	cmd := exec.Command("git", "-c", "core.fsmonitor=false", "update-index", "--add", "fsm.sh")
	cmd.Dir = project
	cmd.Env = append(os.Environ(), "GIT_DIR="+project, "GIT_WORK_TREE="+project)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("plant index: %v\n%s", err, out)
	}
	return marker
}

// Plain git, as kota ran it before, falls back to the planted repository
// and runs its fsmonitor: the attack is real in this environment.
func TestPlantRunsUnderPlainGit(t *testing.T) {
	isolate(t)
	project := t.TempDir()
	git(t, project, "init", "-q")
	marker := plant(t, project)
	exec.Command("git", "-C", project, "ls-files").Run()
	if _, err := os.Stat(marker); err != nil {
		t.Skipf("this git doesn't fall back to the planted repository (%v); nothing to defend against", err)
	}
}

func TestCommandRefusesPlant(t *testing.T) {
	isolate(t)
	for name, init := range map[string]bool{"git project": true, "non-git project": false} {
		t.Run(name, func(t *testing.T) {
			project := t.TempDir()
			if init {
				git(t, project, "init", "-q")
			}
			marker := plant(t, project)
			for _, args := range [][]string{
				{"ls-files", "-z"},
				{"status", "--porcelain"},
				{"rev-parse", "--git-dir"},
			} {
				out, err := Command(project, args...).CombinedOutput()
				if err == nil {
					t.Errorf("git %v succeeded on the plant:\n%s", args, out)
				}
			}
			if _, err := os.Stat(marker); err == nil {
				t.Error("the planted fsmonitor ran")
			}
			for _, key := range []string{"user.name", "core.hooksPath"} {
				out, _ := Command(project, "config", "--get", key).Output()
				if v := strings.TrimSpace(string(out)); v == "AGENT" || v == "/tmp/elsewhere" {
					t.Errorf("%s = %q, from the planted config", key, v)
				}
			}
			out, _ := Command(project, "config", "--list", "--show-origin").Output()
			if strings.Contains(string(out), "AGENT") {
				t.Errorf("config --list reads the planted config:\n%s", out)
			}
		})
	}
}

// A normal repository, a linked worktree's .git file and a project inside
// a repository above it all work.
func TestCommandReadsRepositories(t *testing.T) {
	isolate(t)
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.name", "Repo")
	write(t, filepath.Join(repo, "a.txt"), "a", 0o644)
	git(t, repo, "add", "a.txt")
	git(t, repo, "-c", "user.email=t@example.com", "commit", "-q", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, repo, "worktree", "add", "-q", wt)
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{repo, wt} {
		out, err := Command(dir, "ls-files", "-z").Output()
		if err != nil || string(out) != "a.txt\x00" {
			t.Errorf("%s: ls-files = %q, %v", dir, out, err)
		}
	}
	for dir, want := range map[string]string{repo: "Repo", wt: "Repo", sub: "Repo", t.TempDir(): "Host"} {
		out, err := Command(dir, "config", "--get", "user.name").Output()
		if got := strings.TrimSpace(string(out)); err != nil || got != want {
			t.Errorf("%s: user.name = %q, %v; want %q", dir, got, err, want)
		}
	}
}

func TestConfigFileRunsOutsideRepositories(t *testing.T) {
	isolate(t)
	file := filepath.Join(t.TempDir(), "config")
	write(t, file, "[extensions]\n\tworktreeConfig = true\n", 0o644)
	cmd := ConfigFile(file, "--bool", "--get", "extensions.worktreeConfig")
	if cmd.Dir != "/" {
		t.Errorf("dir = %q", cmd.Dir)
	}
	if out, err := cmd.Output(); err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Errorf("ConfigFile = %q, %v", out, err)
	}
}

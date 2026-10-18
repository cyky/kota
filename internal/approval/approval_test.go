package approval

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestNeedsConfirmation(t *testing.T) {
	dirty := Status{Git: true, Changes: []string{" M main.go"}}
	clean := Status{Git: true}
	cases := []struct {
		name     string
		fullAuto bool
		status   Status
		want     bool
	}{
		{"full auto, dirty", true, dirty, true},
		{"full auto, clean", true, clean, false},
		{"full auto, not git", true, Status{}, false},
		{"ask mode, dirty", false, dirty, false},
		{"ask mode, clean", false, clean, false},
	}
	for _, c := range cases {
		if got := NeedsConfirmation(c.fullAuto, c.status); got != c.want {
			t.Errorf("%s: NeedsConfirmation = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestConfirm(t *testing.T) {
	s := Status{Git: true, Changes: []string{" M main.go", "?? new.txt"}}
	cases := map[string]bool{
		"y\n":   true,
		"Y\n":   true,
		" y \n": true,
		"y":     true,
		"yes\n": false,
		"n\n":   false,
		"\n":    false,
		"":      false,
	}
	for input, want := range cases {
		var out bytes.Buffer
		if got := Confirm(strings.NewReader(input), &out, "the agent", s); got != want {
			t.Errorf("Confirm(%q) = %v, want %v", input, got, want)
		}
		for _, line := range []string{"the agent runs in full auto", "2 uncommitted change(s)", " M main.go", "?? new.txt", "Type y"} {
			if !strings.Contains(out.String(), line) {
				t.Errorf("prompt lacks %q:\n%s", line, out.String())
			}
		}
	}
}

func TestConfirmTruncatesLongLists(t *testing.T) {
	var s Status
	for i := range 25 {
		s.Changes = append(s.Changes, "?? f"+strconv.Itoa(i))
	}
	var out bytes.Buffer
	Confirm(strings.NewReader("n\n"), &out, "the agent", s)
	if strings.Contains(out.String(), "f10") || !strings.Contains(out.String(), "and 15 more") {
		t.Errorf("prompt:\n%s", out.String())
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// hostRunner runs the command on the host. Only tests use it; it checks
// that the command's directory is among the mounts.
func hostRunner(t *testing.T) Runner {
	return func(mounts []string, dir string, cmd ...string) (string, error) {
		if !slices.ContainsFunc(mounts, func(m string) bool { return dir == m || strings.HasPrefix(dir, m+"/") }) {
			t.Errorf("dir %s not in mounts %q", dir, mounts)
		}
		c := exec.Command(cmd[0], cmd[1:]...)
		c.Dir = dir
		out, err := c.Output()
		return string(out), err
	}
}

func isolateGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func TestReadStatus(t *testing.T) {
	isolateGit(t)
	ReadStatus := func(project string) (Status, error) { return ReadStatus(project, hostRunner(t)) }

	plain := t.TempDir()
	if s, err := ReadStatus(plain); err != nil || s.Git {
		t.Errorf("non-git project: %+v, %v", s, err)
	}

	repo := t.TempDir()
	git(t, repo, "init", "-q")
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go", "package main\n")
	write(".gitignore", "build/\n")
	git(t, repo, "add", ".")
	git(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "init")

	if s, err := ReadStatus(repo); err != nil || !s.Git || len(s.Changes) != 0 {
		t.Errorf("clean repo: %+v, %v", s, err)
	}

	write("main.go", "package main // changed\n")
	write("new.txt", "x")
	os.Mkdir(filepath.Join(repo, "build"), 0o755)
	write("build/out", "ignored")
	s, err := ReadStatus(repo)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{" M main.go", "?? new.txt"}; !s.Git || !slices.Equal(s.Changes, want) {
		t.Errorf("dirty repo: %+v, want changes %q", s, want)
	}

	// A subdirectory of a repository is a git project, and only its own
	// changes count.
	sub := filepath.Join(repo, "sub")
	os.Mkdir(sub, 0o755)
	if s, err := ReadStatus(sub); err != nil || !s.Git || len(s.Changes) != 0 {
		t.Errorf("clean subdirectory of a dirty repo: %+v, %v", s, err)
	}
	write("sub/x.txt", "x")
	if s, err := ReadStatus(sub); err != nil || !slices.Equal(s.Changes, []string{"?? sub/"}) {
		t.Errorf("dirty subdirectory: %+v, %v", s, err)
	}
}

func TestConfirmLeavesTheRestOfInput(t *testing.T) {
	in := strings.NewReader("y\nfor the agent\n")
	if !Confirm(in, &bytes.Buffer{}, "the agent", Status{Git: true, Changes: []string{"?? x"}}) {
		t.Fatal("declined")
	}
	rest := new(strings.Builder)
	if _, err := io.Copy(rest, in); err != nil || rest.String() != "for the agent\n" {
		t.Errorf("rest of input = %q, %v", rest.String(), err)
	}
}

func TestReadStatusNeverRunsGitWithoutARepo(t *testing.T) {
	called := false
	run := func([]string, string, ...string) (string, error) { called = true; return "", nil }
	if s, err := ReadStatus(t.TempDir(), run); err != nil || s.Git || called {
		t.Errorf("non-git project: %+v, %v, runner called %v", s, err, called)
	}
}

func TestFindRepo(t *testing.T) {
	isolateGit(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(root, "main")
	os.Mkdir(main, 0o755)
	git(t, main, "init", "-q")
	git(t, main, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	sub := filepath.Join(main, "sub")
	os.Mkdir(sub, 0o755)
	wt := filepath.Join(root, "wt")
	git(t, main, "worktree", "add", "-q", wt)
	os.Mkdir(filepath.Join(wt, "sub"), 0o755)
	plain := filepath.Join(root, "plain")
	os.Mkdir(plain, 0o755)
	linked := filepath.Join(root, "linked")
	os.Mkdir(linked, 0o755)
	os.Symlink(filepath.Join(main, ".git"), filepath.Join(linked, ".git"))

	tests := []struct {
		name    string
		project string
		ok      bool
		mounts  []string
		err     string
	}{
		{name: "repository root", project: main, ok: true, mounts: []string{main}},
		{name: "subdirectory", project: sub, ok: true, mounts: []string{sub, filepath.Join(main, ".git")}},
		{name: "linked worktree", project: wt, ok: true, mounts: []string{wt, filepath.Join(main, ".git")}},
		{name: "worktree subdirectory", project: filepath.Join(wt, "sub"), ok: true,
			mounts: []string{filepath.Join(wt, "sub"), filepath.Join(wt, ".git"), filepath.Join(main, ".git")}},
		{name: "no repository", project: plain},
		{name: "symlinked .git", project: linked, err: "not a directory or file"},
	}
	for _, tt := range tests {
		repo, ok, err := FindRepo(tt.project)
		switch {
		case tt.err != "":
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Errorf("%s: err = %v, want %q", tt.name, err, tt.err)
			}
		case err != nil || ok != tt.ok || !slices.Equal(repo.Mounts, tt.mounts):
			t.Errorf("%s: FindRepo = %q, %v, %v; want %q, %v", tt.name, repo.Mounts, ok, err, tt.mounts, tt.ok)
		}
	}

	// The worktree's status works with only those paths.
	os.WriteFile(filepath.Join(wt, "sub", "x.txt"), []byte("x"), 0o644)
	s, err := ReadStatus(wt, hostRunner(t))
	if err != nil || !slices.Equal(s.Changes, []string{"?? sub/"}) {
		t.Errorf("worktree status: %+v, %v", s, err)
	}
}

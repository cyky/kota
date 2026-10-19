package offlinegit

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"kota/internal/pathpolicy"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// hostRunner runs the job on the host. Only tests use it; it checks that
// the job's directory is among the mounts.
func hostRunner(t *testing.T) Runner {
	return func(j Job) (string, error) {
		mounts := append(slices.Clip(j.ReadOnly), j.Writable)
		if !slices.ContainsFunc(mounts, func(m string) bool { return m != "" && (j.Dir == m || strings.HasPrefix(j.Dir, m+"/")) }) {
			t.Errorf("dir %s not in mounts %q", j.Dir, mounts)
		}
		c := exec.Command(j.Cmd[0], j.Cmd[1:]...)
		c.Dir = j.Dir
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

func TestGitAndScriptHarden(t *testing.T) {
	var got Job
	run := func(j Job) (string, error) { got = j; return "out", nil }
	job := Job{ReadOnly: []string{"/p"}, Writable: "/w", Dir: "/p"}

	out, err := Git(run, job, "-c", "x.y=z", "status")
	want := []string{"git", "-c", "safe.directory=*", "-c", "core.fsmonitor=false", "--no-optional-locks", "-c", "x.y=z", "status"}
	if err != nil || out != "out" || !slices.Equal(got.Cmd, want) || got.Writable != "/w" || got.Dir != "/p" {
		t.Errorf("Git ran %+v, returned %q, %v; want command %q", got, out, err, want)
	}

	if _, err := Script(run, job, `g log "$1"`, "HEAD"); err != nil {
		t.Fatal(err)
	}
	if len(got.Cmd) != 5 || got.Cmd[0] != "sh" || got.Cmd[2] != gitFunc+`g log "$1"` || got.Cmd[4] != "HEAD" {
		t.Errorf("Script ran %q", got.Cmd)
	}
}

// g in a script behaves as Git: the same hardening, and more -c options
// before the subcommand.
func TestScriptGitFunction(t *testing.T) {
	isolateGit(t)
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	out, err := Script(hostRunner(t), Job{ReadOnly: []string{repo}, Dir: repo},
		`g -c kota.test="$1" config --get kota.test; g config --get core.fsmonitor`, "set")
	if err != nil || out != "set\nfalse\n" {
		t.Errorf("script output %q, %v", out, err)
	}
}

func TestDockerChecksEveryMount(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "Projects")
	project := filepath.Join(root, "app")
	for _, dir := range []string{project, filepath.Join(home, ".ssh")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run := Docker("kota-base:latest", os.Getuid(), os.Getgid(), pathpolicy.Default(home, root))
	for name, job := range map[string]Job{
		"read-only": {ReadOnly: []string{project, home}, Dir: project},
		"writable":  {ReadOnly: []string{project}, Writable: filepath.Join(home, ".ssh"), Dir: project},
	} {
		// The check comes before docker runs, so no Docker is needed.
		if _, err := run(job); err == nil || !strings.Contains(err.Error(), "refusing to mount") {
			t.Errorf("%s: err = %v, want the mount refused", name, err)
		}
	}
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

func TestReadStatusNeverRunsGitWithoutARepo(t *testing.T) {
	called := false
	run := func(Job) (string, error) { called = true; return "", nil }
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
	// A submodule's .git file leads to a git dir without commondir.
	modDir := filepath.Join(main, ".git", "modules", "lib")
	os.MkdirAll(modDir, 0o755)
	module := filepath.Join(main, "lib")
	os.Mkdir(module, 0o755)
	os.WriteFile(filepath.Join(module, ".git"), []byte("gitdir: ../.git/modules/lib\n"), 0o644)

	mainGit := filepath.Join(main, ".git")
	tests := []struct {
		name    string
		project string
		ok      bool
		want    Repo
		err     string
	}{
		{name: "repository root", project: main, ok: true,
			want: Repo{Top: main, CommonDir: mainGit, Mounts: []string{main}}},
		{name: "subdirectory", project: sub, ok: true,
			want: Repo{Top: main, CommonDir: mainGit, Mounts: []string{sub, mainGit}}},
		{name: "linked worktree", project: wt, ok: true,
			want: Repo{Top: wt, Linked: true, CommonDir: mainGit, Mounts: []string{wt, mainGit}}},
		{name: "worktree subdirectory", project: filepath.Join(wt, "sub"), ok: true,
			want: Repo{Top: wt, Linked: true, CommonDir: mainGit,
				Mounts: []string{filepath.Join(wt, "sub"), filepath.Join(wt, ".git"), mainGit}}},
		{name: "submodule", project: module, ok: true,
			want: Repo{Top: module, Linked: true, CommonDir: modDir, Mounts: []string{module, modDir}}},
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
		case err != nil || ok != tt.ok || repo.Top != tt.want.Top || repo.Linked != tt.want.Linked ||
			repo.CommonDir != tt.want.CommonDir || !slices.Equal(repo.Mounts, tt.want.Mounts):
			t.Errorf("%s: FindRepo = %+v, %v, %v; want %+v, %v", tt.name, repo, ok, err, tt.want, tt.ok)
		}
	}

	// The worktree's status works with only those paths.
	os.WriteFile(filepath.Join(wt, "sub", "x.txt"), []byte("x"), 0o644)
	s, err := ReadStatus(wt, hostRunner(t))
	if err != nil || !slices.Equal(s.Changes, []string{"?? sub/"}) {
		t.Errorf("worktree status: %+v, %v", s, err)
	}
}

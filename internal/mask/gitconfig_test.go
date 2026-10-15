package mask

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// mountAt returns the planned mount at path.
func mountAt(l Layout, path string) (Mount, bool) {
	i := slices.IndexFunc(l.Mounts, func(m Mount) bool { return m.Path == path })
	if i < 0 {
		return Mount{}, false
	}
	return l.Mounts[i], true
}

func TestConfigIncludesProtected(t *testing.T) {
	isolateGit(t)
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  []Mount
	}{
		{
			name: "include.path",
			setup: func(t *testing.T, dir string) {
				write(t, filepath.Join(dir, ".gitconfig"), "[core]\n\tautocrlf = false\n")
				git(t, dir, "config", "include.path", "../.gitconfig")
			},
			want: []Mount{{Path: ".gitconfig"}},
		},
		{
			name: "missing target",
			setup: func(t *testing.T, dir string) {
				git(t, dir, "config", "include.path", "../config/git/extra.cfg")
			},
			want: []Mount{{Path: "config", Dir: true, Placeholder: true}},
		},
		{
			name: "includeIf whose condition doesn't hold",
			setup: func(t *testing.T, dir string) {
				write(t, filepath.Join(dir, "tools/branch.cfg"), "")
				git(t, dir, "config", "includeIf.onbranch:release.path", "../tools/branch.cfg")
			},
			want: []Mount{{Path: "tools/branch.cfg"}, {Path: "tools", Dir: true, Pin: true}},
		},
		{
			name: "nested include",
			setup: func(t *testing.T, dir string) {
				write(t, filepath.Join(dir, ".gitconfig"), "[include]\n\tpath = shared/more.cfg\n")
				write(t, filepath.Join(dir, "shared/more.cfg"), "")
				git(t, dir, "config", "include.path", "../.gitconfig")
			},
			want: []Mount{{Path: ".gitconfig"}, {Path: "shared/more.cfg"}},
		},
		{
			name: "global includeIf through a file outside the project",
			setup: func(t *testing.T, dir string) {
				outside := filepath.Join(t.TempDir(), "work.cfg")
				write(t, outside, "[include]\n\tpath = "+filepath.Join(dir, "team.cfg")+"\n")
				write(t, os.Getenv("GIT_CONFIG_GLOBAL"), "[includeIf \"gitdir:/nowhere/\"]\n\tpath = "+outside+"\n")
			},
			want: []Mount{{Path: "team.cfg", Placeholder: true}},
		},
		{
			name: "submodule git dir",
			setup: func(t *testing.T, dir string) {
				sub := repo(t)
				git(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "libs/sub")
				git(t, filepath.Join(dir, "libs/sub"), "config", "include.path", "../../../../libs/sub/.gitconfig")
				write(t, filepath.Join(dir, "libs/sub/.gitconfig"), "")
			},
			want: []Mount{{Path: "libs/sub/.gitconfig"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
			dir, err := filepath.EvalSymlinks(repo(t))
			if err != nil {
				t.Fatal(err)
			}
			tt.setup(t, dir)
			l := mustPlan(t, dir)
			for _, want := range tt.want {
				if got, ok := mountAt(l, want.Path); !ok || got != want {
					t.Errorf("mount %s = %+v (planned %v), want %+v", want.Path, got, ok, want)
				}
			}
		})
	}
}

func TestConfigIncludeOutsideLeftAlone(t *testing.T) {
	isolateGit(t)
	dir := repo(t)
	outside := filepath.Join(t.TempDir(), "shared.cfg")
	write(t, outside, "")
	git(t, dir, "config", "include.path", outside)
	want := mustPlan(t, repo(t)).Mounts
	if got := mustPlan(t, dir).Mounts; !slices.Equal(got, want) {
		t.Errorf("mounts = %+v, want the plain repo's %+v", got, want)
	}
}

func TestConfigIncludeSymlinkRefused(t *testing.T) {
	isolateGit(t)
	dir := repo(t)
	write(t, filepath.Join(dir, "real.cfg"), "")
	if err := os.Symlink("real.cfg", filepath.Join(dir, "link.cfg")); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "config", "include.path", "../link.cfg")
	if _, err := Plan(dir, nil); err == nil || !strings.Contains(err.Error(), "symlink") || !strings.Contains(err.Error(), "include.path") {
		t.Errorf("err = %v, want a symlink refusal naming include.path", err)
	}
}

func TestConfigExecRefs(t *testing.T) {
	isolateGit(t)
	dir, err := filepath.EvalSymlinks(repo(t))
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "pager.sh")
	write(t, filepath.Join(dir, "scripts/args.js"), "")
	write(t, filepath.Join(dir, ".gitconfig"), "[filter \"crypt\"]\n\tclean = tools/crypt clean %f\n")
	for _, kv := range [][2]string{
		{"core.fsmonitor", "scripts/fsmonitor.sh"},
		{"alias.fix", "!./bin/fix --all"},
		{"alias.log2", "!git log origin/main"},
		{"alias.st", "status"},
		{"core.pager", outside},
		{"core.editor", "vim"},
		{"diff.json.textconv", "node scripts/args.js"},
		{"core.sshCommand", ".git/hooks/ssh"},
		{"include.path", "../.gitconfig"},
	} {
		git(t, dir, "config", kv[0], kv[1])
	}
	l := mustPlan(t, dir)

	wantWatch := []string{"bin/fix", "scripts/args.js", "scripts/fsmonitor.sh", "tools/crypt"}
	if !slices.Equal(l.Watch, wantWatch) {
		t.Errorf("watch = %q, want %q", l.Watch, wantWatch)
	}
	joined := strings.Join(l.Warnings, "\n")
	for _, want := range []string{
		"git config core.fsmonitor in .git/config runs scripts/fsmonitor.sh",
		"git config alias.fix in .git/config runs bin/fix",
		"git config diff.json.textconv in .git/config runs scripts/args.js",
		"git config filter.crypt.clean in .gitconfig runs tools/crypt",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings lack %q:\n%s", want, joined)
		}
	}
	for _, unwanted := range []string{"origin/main", "pager.sh", ".git/hooks/ssh", "alias.st"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("warnings mention %q:\n%s", unwanted, joined)
		}
	}
}

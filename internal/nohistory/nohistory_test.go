package nohistory

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"kota/internal/offlinegit"
)

func TestParseInspect(t *testing.T) {
	id := strings.Repeat("a", 40)
	out := "100644 blob " + id + "\tREADME\x00" +
		"160000 commit " + id + "\tlib/sub\x00" +
		"100644 blob " + id + "\tdocs/lfs\x00" +
		"lfs\x00" +
		"assets/big.bin\x00lfs\x00"
	m, err := parseInspect(out)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(m.Submodules, []string{"lib/sub"}) {
		t.Errorf("submodules = %q", m.Submodules)
	}
	// A path named "lfs" after the marker is an LFS path, not a marker.
	if !slices.Equal(m.LFS, []string{"assets/big.bin", "lfs"}) {
		t.Errorf("LFS = %q", m.LFS)
	}

	// An empty tree: the marker comes first.
	if m, err := parseInspect("lfs\x00"); err != nil || !m.Empty() {
		t.Errorf("empty tree = %+v, %v", m, err)
	}
	if _, err := parseInspect("100644 blob " + id + "\tREADME\x00"); err == nil {
		t.Error("output without the marker parsed")
	}
}

func TestInspectLeavesOutConfigFiles(t *testing.T) {
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(j offlinegit.Job) (string, error) {
		if slices.ContainsFunc(j.Cmd, func(arg string) bool { return strings.HasSuffix(arg, inspectScript) }) {
			return "lfs\x00", nil
		}
		return " M src/app.ts\n?? kota.yaml\n M .kotaignore\n?? notes.txt\n", nil
	}
	m, err := Inspect(project, run)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{" M src/app.ts", "?? notes.txt"}; !slices.Equal(m.Changes, want) {
		t.Errorf("changes = %q, want %q", m.Changes, want)
	}
}

func TestInspectNeedsRepositoryTop(t *testing.T) {
	run := func(offlinegit.Job) (string, error) {
		t.Error("ran git outside a repository's top")
		return "", nil
	}
	plain := t.TempDir()
	if _, err := Inspect(plain, run); err == nil || !strings.Contains(err.Error(), "isn't in one") {
		t.Errorf("not a repository: %v", err)
	}
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(sub, run); err == nil || !strings.Contains(err.Error(), "inside a larger git repository") {
		t.Errorf("subdirectory: %v", err)
	}
}

func TestConfirm(t *testing.T) {
	m := Missing{Changes: []string{" M a.go"}, Submodules: []string{"lib/sub"}}
	var out bytes.Buffer
	in := strings.NewReader("y\nrest")
	if !Confirm(in, &out, m) {
		t.Error("y declined")
	}
	for _, want := range []string{" M a.go", "lib/sub", "submodules"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("prompt lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "LFS") {
		t.Errorf("prompt lists LFS without LFS files:\n%s", out.String())
	}
	// Nothing after the answer is consumed: the rest belongs to the agent.
	if rest, _ := io.ReadAll(in); string(rest) != "rest" {
		t.Errorf("left %q on stdin", rest)
	}
	for _, answer := range []string{"", "n\n", "yes\n"} {
		if Confirm(strings.NewReader(answer), &out, m) {
			t.Errorf("%q confirmed", answer)
		}
	}
}

func TestTakeConfig(t *testing.T) {
	project, copy := t.TempDir(), t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The project's .kotaignore is untracked: the commit has an older one.
	write(filepath.Join(project, ".kotaignore"), "config/secrets.yml\n")
	write(filepath.Join(copy, ".kotaignore"), "old\n")
	// The commit has a kota.yaml the project has since deleted.
	write(filepath.Join(copy, "kota.yaml"), "engine: {}\n")
	// The commit has a symlink where the project has a Dockerfile.
	write(filepath.Join(project, "kota.Dockerfile"), "FROM base\n")
	write(outside, "keep\n")
	if err := os.Symlink(outside, filepath.Join(copy, "kota.Dockerfile")); err != nil {
		t.Fatal(err)
	}

	if err := TakeConfig(project, copy); err != nil {
		t.Fatal(err)
	}
	read := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			return "<" + err.Error() + ">"
		}
		return string(b)
	}
	if got := read(filepath.Join(copy, ".kotaignore")); got != "config/secrets.yml\n" {
		t.Errorf(".kotaignore = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(copy, "kota.yaml")); !os.IsNotExist(err) {
		t.Errorf("kota.yaml the project lacks is still in the copy: %v", err)
	}
	if info, err := os.Lstat(filepath.Join(copy, "kota.Dockerfile")); err != nil || !info.Mode().IsRegular() {
		t.Errorf("kota.Dockerfile = %v, %v; want a regular file", info, err)
	}
	if got := read(outside); got != "keep\n" {
		t.Errorf("write followed the copy's symlink: outside = %q", got)
	}

	// A symlinked config file in the project is refused, as mask.Plan does.
	if err := os.Remove(filepath.Join(project, ".kotaignore")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(project, ".kotaignore")); err != nil {
		t.Fatal(err)
	}
	if err := TakeConfig(project, copy); err == nil {
		t.Error("symlinked .kotaignore in the project was copied")
	}
}

package engineimages

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"kota/internal/engine"
)

var (
	digestA = "sha256:" + strings.Repeat("a", 64)
	digestB = "sha256:" + strings.Repeat("b", 64)
)

// fakeCrane is a cache whose Run runs the real scripts with sh and a fake
// crane, which logs its calls. A tag resolves to the digest in tags
// (digestA by default); pulling fail fails.
type fakeCrane struct {
	Cache
	log  string
	tags map[string]string
	fail string
}

func newFake(t *testing.T) *fakeCrane {
	t.Helper()
	bin := t.TempDir()
	f := &fakeCrane{log: filepath.Join(bin, "crane.log"), tags: map[string]string{}}
	crane := `#!/bin/sh
printf '%s\n' "$*" >> "$CRANE_LOG"
case "$1" in
  digest) d=$(grep -m1 "^$2 " "$CRANE_TAGS" | cut -d' ' -f2); echo "${d:-` + digestA + `}" ;;
  pull) [ "${4%@*}" = "$CRANE_FAIL" ] && exit 1; printf tar > "$5" ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "crane"), []byte(crane), 0o755); err != nil {
		t.Fatal(err)
	}
	tags := filepath.Join(bin, "tags")
	f.Cache = Cache{
		Dir:      filepath.Join(t.TempDir(), "images"),
		Platform: "linux/arm64",
		Out:      io.Discard,
		Run: func(work, script string) error {
			var lines []string
			for ref, d := range f.tags {
				lines = append(lines, ref+" "+d)
			}
			if err := os.WriteFile(tags, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
				return err
			}
			cmd := exec.Command("sh", "-c", script)
			cmd.Dir = work
			cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "CRANE_LOG=" + f.log, "CRANE_TAGS=" + tags, "CRANE_FAIL=" + f.fail}
			return cmd.Run()
		},
	}
	return f
}

func (f *fakeCrane) calls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(f.log)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		t.Fatal(err)
	}
	os.Remove(f.log)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// prep plans and fetches declared, as one prep does.
func (f *fakeCrane) prep(t *testing.T, declared ...string) *engine.ImagePlan {
	t.Helper()
	p, err := f.Plan(declared)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Fetch(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPlanNoImages(t *testing.T) {
	f := newFake(t)
	p, err := f.Plan(nil)
	if p != nil || err != nil {
		t.Errorf("Plan = %+v, %v; want nothing to do", p, err)
	}
	if calls := f.calls(t); calls != nil {
		t.Errorf("crane ran: %q", calls)
	}
}

// TestPlanLooksUpOnlyRecordedImages: before the user confirms, only images
// the last prep recorded may be looked up, so a reference an agent added
// doesn't reach the network.
func TestPlanLooksUpOnlyRecordedImages(t *testing.T) {
	f := newFake(t)
	p, err := f.Plan([]string{"postgres:16-alpine"})
	if err != nil || !slices.Equal(p.Added, []string{"postgres:16-alpine"}) {
		t.Fatalf("first plan = %+v, %v", p, err)
	}
	if calls := f.calls(t); calls != nil {
		t.Errorf("first prep looked up before the confirmation: %q", calls)
	}
	f.prep(t, "postgres:16-alpine", "redis:7")
	f.calls(t)

	const added = "evil.example/leak:secret"
	f.tags["redis:7"] = digestB
	p, err = f.Plan([]string{"postgres:16-alpine", "redis:7", added})
	if err != nil {
		t.Fatal(err)
	}
	if calls := f.calls(t); !slices.Equal(calls, []string{"digest postgres:16-alpine", "digest redis:7"}) {
		t.Errorf("looked up before the confirmation: %q", calls)
	}
	if !slices.Equal(p.Added, []string{added}) || len(p.Moved) != 1 || p.Moved[0].Ref != "redis:7" || p.Moved[0].To != digestB || len(p.Keep) != 1 {
		t.Errorf("plan = %+v", p)
	}
	if entries, _ := os.ReadDir(filepath.Dir(f.Dir)); len(entries) != 1 {
		t.Errorf("left next to the archives: %v", entries)
	}
}

func TestFetchOnlyNewAndMoved(t *testing.T) {
	f := newFake(t)
	f.prep(t, "postgres:16-alpine", "redis:7")
	if calls := f.calls(t); len(calls) != 4 {
		t.Errorf("first prep = %q, want a lookup and a pull per image", calls)
	}
	rec, err := engine.LoadRecord(f.Dir)
	if err != nil || rec == nil || len(rec.Archives) != 2 {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	kept, err := os.Stat(filepath.Join(f.Dir, engine.ArchiveFile("postgres:16-alpine")))
	if err != nil {
		t.Fatal(err)
	}

	// Unchanged: looked up, nothing downloaded.
	p := f.prep(t, "postgres:16-alpine", "redis:7")
	if p.Changed() || len(p.Downloads) != 0 {
		t.Errorf("unchanged plan = %+v", p)
	}
	for _, c := range f.calls(t) {
		if strings.HasPrefix(c, "pull") {
			t.Errorf("unchanged image downloaded: %s", c)
		}
	}

	// redis moved: only it is pulled, by its new digest; postgres's archive
	// is the same file.
	f.tags["redis:7"] = digestB
	f.prep(t, "postgres:16-alpine", "redis:7")
	var pulls []string
	for _, c := range f.calls(t) {
		if strings.HasPrefix(c, "pull") {
			pulls = append(pulls, c)
		}
	}
	if len(pulls) != 1 || !strings.Contains(pulls[0], "redis@"+digestB) {
		t.Errorf("pulls = %q, want only redis by its new digest", pulls)
	}
	after, err := os.Stat(filepath.Join(f.Dir, engine.ArchiveFile("postgres:16-alpine")))
	if err != nil || !os.SameFile(kept, after) {
		t.Errorf("kept archive replaced: %v", err)
	}
}

func TestFetchFailureKeepsArchives(t *testing.T) {
	f := newFake(t)
	f.prep(t, "postgres:16-alpine")
	before, _ := engine.LoadRecord(f.Dir)
	f.fail = "redis"
	p, err := f.Plan([]string{"postgres:16-alpine", "redis:7"})
	if err != nil {
		t.Fatal(err)
	}
	err = f.Fetch(p)
	if err == nil || !strings.Contains(err.Error(), "download engine image redis:7") {
		t.Fatalf("err = %v, want it to name redis:7", err)
	}
	after, _ := engine.LoadRecord(f.Dir)
	if after == nil || !slices.Equal(after.Archives, before.Archives) {
		t.Errorf("record after a failed download = %+v, want %+v", after, before)
	}
	if entries, _ := os.ReadDir(filepath.Dir(f.Dir)); len(entries) != 1 {
		t.Errorf("staging left: %v", entries)
	}
}

func TestFetchRemovesUndeclared(t *testing.T) {
	f := newFake(t)
	f.prep(t, "postgres:16-alpine")
	p := f.prep(t)
	if !slices.Equal(p.Removed, []string{"postgres:16-alpine"}) {
		t.Errorf("plan = %+v", p)
	}
	if _, err := os.Stat(f.Dir); !os.IsNotExist(err) {
		t.Errorf("archives kept with no images declared: %v", err)
	}
}

// TestFetchRefusesSymlinks: the work directory was written by a container,
// so its digests file and archives must be regular files, never symlinks
// to the host's own files.
func TestFetchRefusesSymlinks(t *testing.T) {
	f := newFake(t)
	secret := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secret, []byte("postgres:16-alpine "+digestA+"\n"), 0o600)
	f.Run = func(work, _ string) error { return os.Symlink(secret, filepath.Join(work, engine.DigestsFile)) }
	p, err := f.Plan([]string{"postgres:16-alpine"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Fetch(p); err == nil || !strings.Contains(err.Error(), "not a non-empty regular file") {
		t.Errorf("err = %v, want a refused symlink", err)
	}
}

func TestFetchNeedsPlatform(t *testing.T) {
	f := newFake(t)
	f.Platform = ""
	p, err := f.Plan([]string{"postgres:16-alpine"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Fetch(p); err == nil || !strings.Contains(err.Error(), "doesn't support this host") {
		t.Errorf("err = %v", err)
	}
	if calls := f.calls(t); calls != nil {
		t.Errorf("crane ran: %q", calls)
	}
}

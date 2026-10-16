package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var (
	digestA = "sha256:" + strings.Repeat("a", 64)
	digestB = "sha256:" + strings.Repeat("b", 64)
)

// stubCrane puts a crane on PATH that logs its calls, resolves
// postgres:16-alpine to digestA and anything else to digestB, writes pulled
// archives, and fails for any reference containing "broken".
func stubCrane(t *testing.T) (log string, env []string) {
	t.Helper()
	bin := t.TempDir()
	log = filepath.Join(bin, "log")
	crane := `#!/bin/sh
printf '%s\n' "$*" >> ` + log + `
case "$*" in *broken*) echo "crane: not found" >&2; exit 1;; esac
case "$1" in
digest) case "$2" in postgres:16-alpine) echo ` + digestA + `;; *) echo ` + digestB + `;; esac;;
pull) : > "$5";;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "crane"), []byte(crane), 0o755); err != nil {
		t.Fatal(err)
	}
	return log, append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
}

func runScript(t *testing.T, script string, env []string) (dir string, err error) {
	t.Helper()
	dir = t.TempDir()
	cmd := exec.Command("sh", "-c", script)
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("script output:\n%s", out)
	}
	return dir, err
}

// TestFetchScript checks a new image is pulled by the digest its tag
// resolves to, and a planned one by its planned digest without a lookup.
func TestFetchScript(t *testing.T) {
	log, env := stubCrane(t)
	downloads := []Archive{
		{Ref: "postgres:16-alpine", File: ArchiveFile("postgres:16-alpine")},
		{Ref: "localhost:5000/team/db:v1", Digest: digestA, File: ArchiveFile("localhost:5000/team/db:v1")},
	}
	dir, err := runScript(t, FetchScript(downloads, "linux/arm64"), env)
	if err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(log)
	want := "digest postgres:16-alpine\n" +
		"pull --platform linux/arm64 postgres@" + digestA + " " + ArchiveFile("postgres:16-alpine") + "\n" +
		"pull --platform linux/arm64 localhost:5000/team/db@" + digestA + " " + ArchiveFile("localhost:5000/team/db:v1") + "\n"
	if string(calls) != want {
		t.Errorf("crane calls:\n%s\nwant:\n%s", calls, want)
	}
	content, err := os.ReadFile(filepath.Join(dir, DigestsFile))
	if err != nil {
		t.Fatal(err)
	}
	digests, err := ParseDigests([]string{"postgres:16-alpine", "localhost:5000/team/db:v1"}, string(content))
	if err != nil {
		t.Fatal(err)
	}
	if digests["postgres:16-alpine"] != digestA || digests["localhost:5000/team/db:v1"] != digestA {
		t.Errorf("digests = %v", digests)
	}
}

func TestResolveScript(t *testing.T) {
	log, env := stubCrane(t)
	dir, err := runScript(t, ResolveScript([]string{"postgres:16-alpine", "redis:7"}), env)
	if err != nil {
		t.Fatal(err)
	}
	if calls, _ := os.ReadFile(log); strings.Contains(string(calls), "pull") {
		t.Errorf("resolving pulled an image:\n%s", calls)
	}
	content, _ := os.ReadFile(filepath.Join(dir, DigestsFile))
	digests, err := ParseDigests([]string{"postgres:16-alpine", "redis:7"}, string(content))
	if err != nil || digests["redis:7"] != digestB {
		t.Errorf("digests = %v, %v", digests, err)
	}
}

// TestScriptsNameFailedImage checks a failed lookup or pull stops the
// script, names the image in FailedFile and records no digest for it.
func TestScriptsNameFailedImage(t *testing.T) {
	_, env := stubCrane(t)
	cases := map[string]string{
		"lookup":         ResolveScript([]string{"redis:7", "broken:1", "nginx"}),
		"pull of new":    FetchScript([]Archive{{Ref: "redis:7"}, {Ref: "broken:1"}, {Ref: "nginx"}}, "linux/arm64"),
		"pull by digest": FetchScript([]Archive{{Ref: "redis:7"}, {Ref: "broken:1", Digest: digestA}, {Ref: "nginx"}}, "linux/arm64"),
	}
	for name, script := range cases {
		t.Run(name, func(t *testing.T) {
			dir, err := runScript(t, script, env)
			if err == nil {
				t.Fatal("script succeeded although crane failed")
			}
			failed, _ := os.ReadFile(filepath.Join(dir, FailedFile))
			if string(failed) != "broken:1\n" {
				t.Errorf("%s = %q, want the failed image", FailedFile, failed)
			}
			digests, _ := os.ReadFile(filepath.Join(dir, DigestsFile))
			if strings.Contains(string(digests), "broken") || strings.Contains(string(digests), "nginx") {
				t.Errorf("digests after the failure: %q", digests)
			}
		})
	}
}

func TestParseDigests(t *testing.T) {
	images := []string{"redis", "postgres:16"}
	cases := map[string]string{
		"missing image":    "redis " + digestA + "\n",
		"bad digest":       "redis sha256:xyz\npostgres:16 " + digestB + "\n",
		"undeclared image": "redis " + digestA + "\npostgres:16 " + digestB + "\nevil " + digestA + "\n",
		"duplicate":        "redis " + digestA + "\nredis " + digestB + "\npostgres:16 " + digestB + "\n",
	}
	for name, content := range cases {
		if _, err := ParseDigests(images, content); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	d, err := ParseDigests(images, "postgres:16 "+digestB+"\nredis "+digestA+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if d["redis"] != digestA || d["postgres:16"] != digestB {
		t.Errorf("digests = %v", d)
	}
	if d, err := ParseDigests(nil, ""); err != nil || len(d) != 0 {
		t.Errorf("no images: %v, %v", d, err)
	}
}

func archive(ref, digest string) Archive {
	return Archive{Ref: ref, Digest: digest, File: ArchiveFile(ref)}
}

func refs(as []Archive) []string {
	var out []string
	for _, a := range as {
		out = append(out, a.Ref)
	}
	return out
}

func TestPlan(t *testing.T) {
	digestC := "sha256:" + strings.Repeat("c", 64)
	last := &Record{Archives: []Archive{archive("postgres:16", digestA), archive("redis:7", digestB)}}
	same := map[string]string{"postgres:16": digestA, "redis:7": digestB}
	all := func(Archive) bool { return true }
	cases := []struct {
		name      string
		declared  []string
		last      *Record
		resolved  map[string]string
		have      func(Archive) bool
		added     []string
		removed   []string
		moved     []Move
		downloads []Archive
		keep      []string
	}{
		{
			name: "first prep", declared: []string{"postgres:16", "redis:7"}, have: all,
			added:     []string{"postgres:16", "redis:7"},
			downloads: []Archive{archive("postgres:16", ""), archive("redis:7", "")},
		},
		{
			name: "unchanged", declared: []string{"postgres:16", "redis:7"}, last: last, resolved: same, have: all,
			keep: []string{"postgres:16", "redis:7"},
		},
		{
			name: "added", declared: []string{"postgres:16", "redis:7", "nginx"}, last: last, resolved: same, have: all,
			added: []string{"nginx"}, downloads: []Archive{archive("nginx", "")}, keep: []string{"postgres:16", "redis:7"},
		},
		{
			name: "removed", declared: []string{"redis:7"}, last: last, resolved: same, have: all,
			removed: []string{"postgres:16"}, keep: []string{"redis:7"},
		},
		{
			name: "moved tag", declared: []string{"postgres:16", "redis:7"}, last: last,
			resolved: map[string]string{"postgres:16": digestC, "redis:7": digestB}, have: all,
			moved:     []Move{{Ref: "postgres:16", From: digestA, To: digestC}},
			downloads: []Archive{archive("postgres:16", digestC)}, keep: []string{"redis:7"},
		},
		{
			name: "archive gone", declared: []string{"postgres:16", "redis:7"}, last: last, resolved: same,
			have:      func(a Archive) bool { return a.Ref != "redis:7" },
			downloads: []Archive{archive("redis:7", digestB)}, keep: []string{"postgres:16"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Plan(c.declared, c.last, c.resolved, c.have)
			if !slices.Equal(p.Added, c.added) || !slices.Equal(p.Removed, c.removed) || !slices.Equal(p.Moved, c.moved) {
				t.Errorf("added %q removed %q moved %+v; want %q %q %+v", p.Added, p.Removed, p.Moved, c.added, c.removed, c.moved)
			}
			if !slices.Equal(p.Downloads, c.downloads) {
				t.Errorf("downloads = %+v, want %+v", p.Downloads, c.downloads)
			}
			if !slices.Equal(refs(p.Keep), c.keep) {
				t.Errorf("keep = %q, want %q", refs(p.Keep), c.keep)
			}
			if p.Changed() != (len(c.added)+len(c.removed)+len(c.moved) > 0) {
				t.Errorf("Changed = %v", p.Changed())
			}
		})
	}
}

func TestPlanRecord(t *testing.T) {
	digestC := "sha256:" + strings.Repeat("c", 64)
	last := &Record{Archives: []Archive{archive("postgres:16", digestA), archive("redis:7", digestB), archive("old", digestA)}}
	declared := []string{"nginx", "redis:7", "postgres:16"}
	p := Plan(declared, last, map[string]string{"postgres:16": digestC, "redis:7": digestB, "old": digestA}, func(Archive) bool { return true })

	r, err := p.Record(map[string]string{"nginx": digestB, "postgres:16": digestC})
	if err != nil {
		t.Fatal(err)
	}
	want := []Archive{archive("nginx", digestB), archive("redis:7", digestB), archive("postgres:16", digestC)}
	if !slices.Equal(r.Archives, want) {
		t.Errorf("record = %+v, want %+v", r.Archives, want)
	}
	if _, err := p.Record(map[string]string{"postgres:16": digestC}); err == nil || !strings.Contains(err.Error(), "nginx") {
		t.Errorf("record without a downloaded image's digest: %v", err)
	}
	if _, err := p.Record(map[string]string{"nginx": digestB, "postgres:16": digestA}); err == nil || !strings.Contains(err.Error(), "postgres:16") {
		t.Errorf("record of a moved image pulled by another digest: %v", err)
	}
	if r, err := Plan(nil, last, nil, nil).Record(nil); err != nil || len(r.Archives) != 0 {
		t.Errorf("record with every image removed = %+v, %v", r, err)
	}
}

func TestRecordSelect(t *testing.T) {
	dir := t.TempDir()
	r, err := LoadRecord(dir)
	if err != nil || r != nil {
		t.Fatalf("LoadRecord on empty dir = %+v, %v", r, err)
	}
	if _, err := r.Select([]string{"redis"}); err == nil || !strings.Contains(err.Error(), "redis") {
		t.Errorf("Select without a record = %v", err)
	}
	rec := Record{Archives: []Archive{{Ref: "redis", Digest: digestA, File: ArchiveFile("redis")}}}
	if err := rec.Save(dir); err != nil {
		t.Fatal(err)
	}
	r, err = LoadRecord(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Select([]string{"redis"})
	if err != nil || len(got) != 1 || got[0].Digest != digestA {
		t.Errorf("Select = %+v, %v", got, err)
	}
	if _, err := r.Select([]string{"redis", "postgres:16"}); err == nil || !strings.Contains(err.Error(), "postgres:16") {
		t.Errorf("Select with an unprepped image = %v", err)
	}
}

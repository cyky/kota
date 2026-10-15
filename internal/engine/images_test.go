package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var (
	digestA = "sha256:" + strings.Repeat("a", 64)
	digestB = "sha256:" + strings.Repeat("b", 64)
)

// TestFetchScript runs the script with a stub crane and checks each image
// is pulled by the digest its tag resolved to.
func TestFetchScript(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "log")
	crane := `#!/bin/sh
printf '%s\n' "$*" >> ` + log + `
case "$1" in
digest) case "$2" in postgres:16-alpine) echo ` + digestA + `;; *) echo ` + digestB + `;; esac;;
pull) : > "$5";;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "crane"), []byte(crane), 0o755); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	images := []string{"postgres:16-alpine", "localhost:5000/team/db:v1"}
	cmd := exec.Command("sh", "-c", FetchScript(images, "linux/arm64"))
	cmd.Dir = out
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	calls, _ := os.ReadFile(log)
	want := "digest postgres:16-alpine\n" +
		"pull --platform linux/arm64 postgres@" + digestA + " " + ArchiveFile("postgres:16-alpine") + "\n" +
		"digest localhost:5000/team/db:v1\n" +
		"pull --platform linux/arm64 localhost:5000/team/db@" + digestB + " " + ArchiveFile("localhost:5000/team/db:v1") + "\n"
	if string(calls) != want {
		t.Errorf("crane calls:\n%s\nwant:\n%s", calls, want)
	}
	digests, err := os.ReadFile(filepath.Join(out, DigestsFile))
	if err != nil {
		t.Fatal(err)
	}
	r, err := ParseDigests(images, string(digests))
	if err != nil {
		t.Fatal(err)
	}
	if r.Archives[0] != (Archive{Ref: "postgres:16-alpine", Digest: digestA, File: ArchiveFile("postgres:16-alpine")}) {
		t.Errorf("record = %+v", r)
	}
}

func TestFetchScriptStopsOnFailure(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "crane"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", FetchScript([]string{"redis"}, "linux/arm64"))
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	if err := cmd.Run(); err == nil {
		t.Error("script succeeded although crane failed")
	}
	if _, err := os.Stat(filepath.Join(cmd.Dir, DigestsFile)); err == nil {
		t.Error("digest recorded for a failed download")
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
	r, err := ParseDigests(images, "postgres:16 "+digestB+"\nredis "+digestA+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if r.Archives[0].Ref != "redis" || r.Archives[1].Digest != digestB {
		t.Errorf("record = %+v", r)
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

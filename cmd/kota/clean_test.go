package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"kota/internal/engine"
	"kota/internal/hostdata"
	"kota/internal/prepgate"
	"kota/internal/sandbox"
	"kota/internal/sessionregistry"
)

// testPrepRecord is project's prep record file.
func testPrepRecord(t *testing.T, project string) string {
	t.Helper()
	file, err := prepRecordFile(project)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func fakeVolumeDocker(t *testing.T, volumes []string) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TEST_VOLUME_LOG"
case "$1 $2" in
  'volume ls')
    if [ "$TEST_VOLUME_LIST_ERROR" = 1 ]; then echo 'listing failed' >&2; exit 1; fi
    printf '%s\n' "$TEST_VOLUME_LIST" ;;
  'volume rm')
    if [ "$TEST_VOLUME_REMOVE_ERROR" = 1 ]; then echo 'volume is in use' >&2; exit 1; fi ;;
  *) echo 'unexpected Docker call' >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TEST_VOLUME_LOG", log)
	t.Setenv("TEST_VOLUME_LIST", strings.Join(volumes, "\n"))
	t.Setenv("TEST_VOLUME_LIST_ERROR", "")
	t.Setenv("TEST_VOLUME_REMOVE_ERROR", "")
	return log
}

func TestCleanProject(t *testing.T) {
	for _, c := range []struct {
		name, answer      string
		shared, listError bool
		removeError       bool
		wantErr           string
		wantRemove        bool
	}{
		{name: "confirm", answer: "y\n", wantRemove: true},
		{name: "yes", answer: "yes\n", wantRemove: true},
		{name: "decline", answer: "n\n", wantErr: "clean not run"},
		{name: "EOF", wantErr: "clean not run"},
		{name: "blank", answer: "\n", wantErr: "clean not run"},
		{name: "shared caches", answer: "y\n", shared: true, wantRemove: true},
		{name: "listing error", answer: "y\n", listError: true, wantErr: "list volumes"},
		{name: "in use", answer: "y\n", removeError: true, wantRemove: true, wantErr: "remove volumes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			project := fakeHome(t, map[string]string{"README.md": "test"})
			own := []string{sandbox.StateVolume(project, "opencode"), sandbox.StateVolume(project, "pi"), sandbox.DepVolume(project, "removed-package/node_modules")}
			other := sandbox.StateVolume(project+"-other", "opencode")
			all := append(append(slices.Clone(own), other, "unrelated-volume"), sandbox.SharedCacheVolumes()...)
			log := fakeVolumeDocker(t, all)
			if c.listError {
				t.Setenv("TEST_VOLUME_LIST_ERROR", "1")
			}
			if c.removeError {
				t.Setenv("TEST_VOLUME_REMOVE_ERROR", "1")
			}
			record := testPrepRecord(t, project)
			if err := (prepgate.Record{}).Save(record); err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			err := cleanProject(project, c.shared, strings.NewReader(c.answer), &out)
			if c.wantErr == "" && err != nil || c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("clean = %v, want %q", err, c.wantErr)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if c.wantRemove {
				want := slices.Clone(own)
				if c.shared {
					want = append(want, sandbox.SharedCacheVolumes()...)
				}
				slices.Sort(want)
				if !strings.Contains(string(calls), "volume rm -f "+strings.Join(want, " ")+"\n") {
					t.Errorf("wrong removal arguments: %s", calls)
				}
			} else if strings.Contains(string(calls), "volume rm") {
				t.Errorf("deleted volumes without confirmation: %s", calls)
			}
			if !c.listError {
				for _, volume := range own {
					if !strings.Contains(out.String(), volume) {
						t.Errorf("confirmation didn't list %s: %s", volume, out.String())
					}
				}
				if !strings.Contains(out.String(), "Type y to clean:") {
					t.Errorf("missing confirmation: %s", out.String())
				}
			}
			_, statErr := os.Stat(record)
			if c.wantErr == "" && !os.IsNotExist(statErr) || c.wantErr != "" && statErr != nil {
				t.Errorf("prep record state after clean = %v", statErr)
			}
			if strings.Contains(out.String(), other) || strings.Contains(out.String(), "unrelated-volume") {
				t.Errorf("unrelated volumes in cleanup plan: %s", out.String())
			}
		})
	}
}

func TestCleanEmptyAndRecordOnly(t *testing.T) {
	project := fakeHome(t, map[string]string{"README.md": "test"})
	log := fakeVolumeDocker(t, nil)
	var out strings.Builder
	if err := cleanProject(project, false, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing to clean") || strings.Contains(out.String(), "Type y") {
		t.Errorf("empty cleanup = %s", out.String())
	}
	record := testPrepRecord(t, project)
	if err := (prepgate.Record{}).Save(record); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := cleanProject(project, false, strings.NewReader("y\n"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "prep confirmation record") {
		t.Errorf("record-only cleanup = %s", out.String())
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Errorf("prep record remains: %v", err)
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), "volume rm") {
		t.Errorf("empty volume removal attempted: %s", calls)
	}
}

func TestCleanEngineData(t *testing.T) {
	project := fakeHome(t, map[string]string{"README.md": "test"})
	log := fakeVolumeDocker(t, nil)
	layout, err := engineLayout()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.ArchiveDir(project), 0o700); err != nil {
		t.Fatal(err)
	}
	archive := engine.Archive{Ref: "postgres:16-alpine", Digest: "sha256:" + strings.Repeat("a", 64), File: engine.ArchiveFile("postgres:16-alpine")}
	if err := (engine.Record{Archives: []engine.Archive{archive}}).Save(layout.ArchiveDir(project)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.ArchiveDir(project), archive.File), []byte("tar"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirs, err := hostdata.Default()
	if err != nil {
		t.Fatal(err)
	}
	root := dirs.Sessions()
	name := sandbox.ProjectName(project)
	// A session that didn't end cleanly, with its certificates; another
	// project's, whose name extends this one's; and the shared base disk.
	leftover := filepath.Join(root, name+"-4242-123456")
	other := filepath.Join(root, name+"-12345678-4242-123456")
	otherProject := layout.ProjectDir(project + "-other")
	base := filepath.Join(layout.BaseDir(), "disk.qcow2")
	for _, f := range []string{filepath.Join(leftover, "engine-client", "key.pem"), filepath.Join(other, "engine-client", "key.pem"), filepath.Join(otherProject, "images", "x.tar"), base} {
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var out strings.Builder
	if err := cleanProject(project, false, strings.NewReader("n\n"), &out); err == nil {
		t.Fatal("clean ran without confirmation")
	}
	for _, want := range []string{layout.ProjectDir(project), "image archive postgres:16-alpine", leftover, "TLS certificates", "Type y to clean:"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("confirmation lacks %q:\n%s", want, out.String())
		}
	}
	for _, unrelated := range []string{other, otherProject, layout.BaseDir()} {
		if strings.Contains(out.String(), unrelated) {
			t.Errorf("confirmation lists %s:\n%s", unrelated, out.String())
		}
	}
	if _, err := os.Stat(layout.ArchiveDir(project)); err != nil {
		t.Errorf("declined clean removed the archives: %v", err)
	}

	out.Reset()
	if err := cleanProject(project, false, strings.NewReader("y\n"), &out); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{layout.ProjectDir(project), leftover} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s remains: %v", gone, err)
		}
	}
	for _, kept := range []string{other, otherProject, base} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s removed: %v", kept, err)
		}
	}
	if calls, _ := os.ReadFile(log); strings.Contains(string(calls), "volume rm") {
		t.Errorf("empty volume removal attempted: %s", calls)
	}

	out.Reset()
	if err := cleanProject(project, false, strings.NewReader(""), &out); err != nil || !strings.Contains(out.String(), "nothing to clean") {
		t.Errorf("second clean = %v:\n%s", err, out.String())
	}
}

func TestCleanNoHistoryCopies(t *testing.T) {
	project := fakeHome(t, map[string]string{"README.md": "test"})
	state := sandbox.StateVolume(project, "pi"+sandbox.NoHistoryState)
	log := fakeVolumeDocker(t, []string{state})
	dirs, err := hostdata.Default()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	kept := dirs.CopyDir(project, start)
	other := dirs.CopyDir(project+"-other", start)
	for _, dir := range []string{kept, other} {
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var out strings.Builder
	if err := cleanProject(project, false, strings.NewReader("y\n"), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Kept --no-history copies", kept, state} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("confirmation lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), other) {
		t.Errorf("confirmation lists another project's copy:\n%s", out.String())
	}
	if _, err := os.Stat(kept); !os.IsNotExist(err) {
		t.Errorf("kept copy remains: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("another project's copy removed: %v", err)
	}
	if calls, _ := os.ReadFile(log); !strings.Contains(string(calls), "volume rm -f "+state+"\n") {
		t.Errorf("no-history state volume not removed: %s", calls)
	}
}

func TestCleanRefusesActiveProject(t *testing.T) {
	project := fakeHome(t, map[string]string{"README.md": "test"})
	log := fakeVolumeDocker(t, nil)
	lease, err := lockProject(sessionArgs{dir: project, agent: defaultAgent}, "run")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseProject(lease)
	var busy *sessionregistry.Busy
	if err := cmdClean([]string{project}); !errors.As(err, &busy) {
		t.Fatalf("clean active project = %v", err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Errorf("busy cleanup called Docker: %v", err)
	}
}

func TestCleanArgumentsAndExclusiveCaches(t *testing.T) {
	project := fakeHome(t, map[string]string{"kota.yaml": "broken: [", "README.md": "test"})
	fakeVolumeDocker(t, nil)
	// No session preparation/model discovery is needed, even if project
	// configuration is broken or the configured gateway key file is missing.
	t.Setenv("KOTA_GATEWAY_KEY_FILE", filepath.Join(t.TempDir(), "missing"))
	t.Chdir(project)
	for _, args := range [][]string{nil, {project}, {"--shared-caches", project}, {project, "--shared-caches"}} {
		if err := cmdClean(args); err != nil {
			t.Fatalf("clean %q = %v", args, err)
		}
	}
	for _, args := range [][]string{{project, "extra"}, {"--auto"}, {"--agent", "pi"}, {"--unknown"}} {
		if err := cmdClean(args); err == nil {
			t.Errorf("invalid clean arguments accepted: %q", args)
		}
	}
	other := filepath.Join(filepath.Dir(project), "other")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	lease, err := lockProject(sessionArgs{dir: other}, "prep")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseProject(lease)
	if err := cmdClean([]string{project}); err != nil {
		t.Errorf("ordinary cleanup blocked by other project: %v", err)
	}
	var busy *sessionregistry.CachesBusy
	if err := cmdClean([]string{project, "--shared-caches"}); !errors.As(err, &busy) {
		t.Errorf("shared-cache reset allowed while another project active: %v", err)
	}
}

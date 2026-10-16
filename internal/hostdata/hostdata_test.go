package hostdata

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"kota/internal/engine"
	"kota/internal/sandbox"
)

func TestDefaultHasNoFallback(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	if d, err := Default(); err == nil {
		t.Errorf("Default = %+v without a home; want an error, not a shared fallback", d)
	}
}

func TestPathsStayUnderRoot(t *testing.T) {
	d := Dirs{Root: "/data/kota"}
	for _, p := range []string{d.Engine().Root, d.Registry(), d.Sessions(), d.PrepRecord("/p/app"), d.CopyDir("/p/app", time.Now())} {
		if !strings.HasPrefix(p, d.Root+"/") {
			t.Errorf("%s outside %s", p, d.Root)
		}
	}
	if d.PrepRecord("/p/app") == d.PrepRecord("/p/app2") {
		t.Error("two projects share a prep record")
	}
}

// TestSessionDirs checks NewSessionDir's names are what SessionDirs finds,
// and that a project whose name extends this one's is left out.
func TestSessionDirs(t *testing.T) {
	d := Dirs{Root: t.TempDir()}
	project := "/p/app"
	mine, err := d.NewSessionDir(project)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(mine); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("session dir %s: %v", mine, err)
	}
	// A project named "app-<8 hex digits>" gets a name that starts with
	// this project's name and a dash.
	other := filepath.Join(d.Sessions(), sandbox.ProjectName(project)+"-12345678-4242-123456")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := d.SessionDirs(project)
	if err != nil || !slices.Equal(got, []string{mine}) {
		t.Errorf("SessionDirs = %q, %v; want %q", got, err, mine)
	}
	if got, err := (Dirs{Root: t.TempDir()}).SessionDirs(project); err != nil || got != nil {
		t.Errorf("no sessions dir: %q, %v", got, err)
	}
}

func TestProjectData(t *testing.T) {
	d := Dirs{Root: t.TempDir()}
	project := "/p/app"
	data, err := d.ProjectData(project)
	if err != nil || !data.Empty() || data.Paths() != nil {
		t.Fatalf("fresh project = %+v, %v", data, err)
	}

	record := d.PrepRecord(project)
	layout := d.Engine()
	archive := engine.Archive{Ref: "postgres:16-alpine", Digest: "sha256:" + strings.Repeat("a", 64), File: engine.ArchiveFile("postgres:16-alpine")}
	for _, dir := range []string{filepath.Dir(record), layout.ArchiveDir(project), layout.BaseDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(record, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (engine.Record{Archives: []engine.Archive{archive}}).Save(layout.ArchiveDir(project)); err != nil {
		t.Fatal(err)
	}
	session, err := d.NewSessionDir(project)
	if err != nil {
		t.Fatal(err)
	}
	// Another project's data and the shared base disk aren't listed.
	if _, err := d.NewSessionDir(project + "-other"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.ProjectDir(project+"-other"), 0o700); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 16, 14, 32, 5, 0, time.Local)
	copyDir := d.CopyDir(project, start)
	if want := sandbox.ProjectName(project) + "-2026-10-16T143205"; filepath.Base(copyDir) != want {
		t.Errorf("copy dir = %s, want %s", copyDir, want)
	}
	for _, dir := range []string{copyDir, d.CopyDir(project+"-other", start), copyDir + "-other"} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	data, err = d.ProjectData(project)
	if err != nil {
		t.Fatal(err)
	}
	if data.Empty() || data.PrepRecord != record || data.EngineDir != layout.ProjectDir(project) ||
		!slices.Equal(data.Images, []string{"postgres:16-alpine"}) || !slices.Equal(data.Sessions, []string{session}) ||
		!slices.Equal(data.Copies, []string{copyDir}) {
		t.Errorf("data = %+v", data)
	}
	// The prep record goes last, so a failed removal keeps the next prep's
	// confirmation.
	if want := []string{layout.ProjectDir(project), session, copyDir, record}; !slices.Equal(data.Paths(), want) {
		t.Errorf("paths = %q, want %q", data.Paths(), want)
	}
}

func TestProjectDataUnreadableRecord(t *testing.T) {
	d := Dirs{Root: t.TempDir()}
	project := "/p/app"
	dir := d.Engine().ArchiveDir(project)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, engine.RecordFile), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := d.ProjectData(project)
	if err != nil || data.EngineDir == "" || data.Images != nil {
		t.Errorf("data = %+v, %v; want the directory listed without images", data, err)
	}
}

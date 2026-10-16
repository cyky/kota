package engine

import (
	"cmp"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	const img = "engine:\n  images: [redis]\n"
	cases := []struct {
		name    string
		yaml    string
		want    []string
		cpus    int // 0: the default
		mem     int // 0: the default
		wantErr string
	}{
		{name: "empty file", yaml: ""},
		{name: "no engine section", yaml: "prep:\n  command: make\n"},
		{name: "images", yaml: "engine:\n  images: [postgres:16-alpine, redis]\n", want: []string{"postgres:16-alpine", "redis"}},
		{name: "with other sections", yaml: "prep:\n  dirs: [.venv]\nengine:\n  images:\n    - ghcr.io/org/app:1.2\n", want: []string{"ghcr.io/org/app:1.2"}},
		{name: "registry with port", yaml: "engine:\n  images: [localhost:5000/team/db:v1]\n", want: []string{"localhost:5000/team/db:v1"}},
		{name: "empty section", yaml: "engine:\n", wantErr: "at least one image"},
		{name: "no images", yaml: "engine:\n  images: []\n", wantErr: "at least one image"},
		{name: "unknown key", yaml: img + "  imgaes: [x]\n", wantErr: `unknown key "imgaes" (want images, cpus, memory)`},
		{name: "unknown key before images", yaml: "engine:\n  disk: 10G\n  images: [redis]\n", wantErr: `unknown key "disk"`},
		{name: "not a mapping", yaml: "engine: [redis]\n", wantErr: "engine: want keys images, cpus, memory"},
		{name: "images not a list", yaml: "engine:\n  images: redis\n", wantErr: "engine.images: want a list"},
		{name: "size", yaml: img + "  cpus: 4\n  memory: 6G\n", want: []string{"redis"}, cpus: 4, mem: 6144},
		{name: "memory in MiB", yaml: img + "  memory: 3072M\n", want: []string{"redis"}, mem: 3072},
		{name: "memory with long unit", yaml: img + "  memory: 1 GiB\n", want: []string{"redis"}, mem: 1024},
		{name: "minimum size", yaml: img + "  cpus: 1\n  memory: 512MiB\n", want: []string{"redis"}, cpus: 1, mem: 512},
		{name: "zero cpus", yaml: img + "  cpus: 0\n", wantErr: `engine.cpus: "0": want a whole number`},
		{name: "fractional cpus", yaml: img + "  cpus: 1.5\n", wantErr: "engine.cpus"},
		{name: "quoted cpus", yaml: img + "  cpus: \"4\"\n", wantErr: "engine.cpus"},
		{name: "cpus list", yaml: img + "  cpus: [4]\n", wantErr: "engine.cpus"},
		{name: "memory without unit", yaml: img + "  memory: 4096\n", wantErr: `engine.memory: "4096": want a size with a unit`},
		{name: "memory in bytes", yaml: img + "  memory: 4GB\n", wantErr: "engine.memory"},
		{name: "memory too small", yaml: img + "  memory: 256M\n", wantErr: "at least 512M"},
		{name: "negative memory", yaml: img + "  memory: -1G\n", wantErr: "engine.memory"},
		{name: "size without images", yaml: "engine:\n  cpus: 4\n", wantErr: "at least one image"},
		{name: "digest", yaml: "engine:\n  images: [postgres@sha256:" + strings.Repeat("a", 64) + "]\n", wantErr: "not a digest"},
		{name: "upper case", yaml: "engine:\n  images: [Postgres:16]\n", wantErr: "not a valid image reference"},
		{name: "shell characters", yaml: "engine:\n  images: [\"redis;rm -rf /\"]\n", wantErr: "not a valid image reference"},
		{name: "option", yaml: "engine:\n  images: [--insecure]\n", wantErr: "not a valid image reference"},
		{name: "duplicate", yaml: "engine:\n  images: [redis, redis]\n", wantErr: "twice"},
		{name: "broken yaml", yaml: "engine: [\n", wantErr: "parse kota.yaml"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := Parse([]byte(c.yaml))
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.want == nil {
				if cfg != nil {
					t.Fatalf("cfg = %+v, want no engine", cfg)
				}
				return
			}
			if !slices.Equal(cfg.Images, c.want) {
				t.Errorf("images = %q, want %q", cfg.Images, c.want)
			}
			wantCPUs, wantMem := cmp.Or(c.cpus, DefaultCPUs), cmp.Or(c.mem, DefaultMemoryMiB)
			if cfg.CPUs != wantCPUs || cfg.MemoryMiB != wantMem {
				t.Errorf("size = %d CPUs, %d MiB, want %d, %d", cfg.CPUs, cfg.MemoryMiB, wantCPUs, wantMem)
			}
		})
	}
}

func TestReadMissingFile(t *testing.T) {
	cfg, err := Read(t.TempDir())
	if err != nil || cfg != nil {
		t.Fatalf("Read = %+v, %v; want no engine", cfg, err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte("engine:\n  images: [redis:7]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = Read(dir)
	if err != nil || cfg == nil || cfg.Images[0] != "redis:7" {
		t.Fatalf("Read = %+v, %v", cfg, err)
	}
}

func TestSplitRef(t *testing.T) {
	cases := map[string][2]string{
		"postgres:16-alpine":        {"postgres", "16-alpine"},
		"redis":                     {"redis", "latest"},
		"localhost:5000/team/db":    {"localhost:5000/team/db", "latest"},
		"localhost:5000/team/db:v1": {"localhost:5000/team/db", "v1"},
	}
	for ref, want := range cases {
		repo, tag := SplitRef(ref)
		if repo != want[0] || tag != want[1] {
			t.Errorf("SplitRef(%q) = %q, %q; want %q", ref, repo, tag, want)
		}
	}
}

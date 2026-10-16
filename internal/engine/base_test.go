package engine

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestUserData(t *testing.T) {
	ud := UserData("http://10.0.2.2:1234/token/")
	if !strings.HasPrefix(ud, "#cloud-config\n") {
		t.Fatal("not a cloud-config")
	}
	var cfg struct {
		Packages   []string `yaml:"packages"`
		WriteFiles []struct {
			Path    string `yaml:"path"`
			Content string `yaml:"content"`
		} `yaml:"write_files"`
		Runcmd     [][]string `yaml:"runcmd"`
		PowerState struct {
			Mode string `yaml:"mode"`
		} `yaml:"power_state"`
	}
	if err := yaml.Unmarshal([]byte(ud), &cfg); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range cfg.WriteFiles {
		files[f.Path] = f.Content
	}
	if files["/etc/sysctl.d/90-engine-ports.conf"] != "net.ipv4.ip_local_port_range = 40000 40099\n" {
		t.Errorf("port range = %q", files["/etc/sysctl.d/90-engine-ports.conf"])
	}
	if FirstPublishedPort != 40000 || LastPublishedPort != 40099 {
		t.Error("the forwarded range differs from the base disk's port range")
	}
	if dropIn := files["/etc/systemd/system/docker.service.d/tcp.conf"]; !strings.Contains(dropIn, "--tlsverify") || !strings.Contains(dropIn, "tcp://0.0.0.0:2376") {
		t.Errorf("docker drop-in = %q", dropIn)
	}
	if len(cfg.Runcmd) != 1 || !strings.Contains(cfg.Runcmd[0][2], `seed="http://10.0.2.2:1234/token/"`) {
		t.Errorf("runcmd = %q", cfg.Runcmd)
	}
	if cfg.PowerState.Mode != "poweroff" {
		t.Errorf("power_state = %+v", cfg.PowerState)
	}
}

func TestLoadBase(t *testing.T) {
	l := Layout{Root: t.TempDir()}
	if _, _, err := l.LoadBase("arm64"); !errors.Is(err, ErrNoBase) {
		t.Fatalf("LoadBase without a disk = %v", err)
	}
	dir := l.BaseDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{BaseDisk, BaseKernel, BaseInitrd} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	def, err := BaseDefinition("arm64")
	if err != nil {
		t.Fatal(err)
	}
	write := func(info BaseInfo) {
		data, _ := json.Marshal(info)
		if err := os.WriteFile(filepath.Join(dir, BaseFile), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(BaseInfo{Definition: def, Arch: "arm64", Root: "PARTUUID=0b6ff0a5-3d1e-4e57-a02c-4ad3c2a3b8a1"})
	b, warning, err := l.LoadBase("arm64")
	if err != nil || warning != "" || b.Root != "PARTUUID=0b6ff0a5-3d1e-4e57-a02c-4ad3c2a3b8a1" || b.Kernel != filepath.Join(dir, BaseKernel) {
		t.Errorf("LoadBase = %+v, %q, %v", b, warning, err)
	}
	if _, _, err := l.LoadBase("amd64"); err == nil {
		t.Error("loaded an arm64 disk for amd64")
	}
	write(BaseInfo{Definition: "old", Arch: "arm64", Root: "PARTUUID=0b6ff0a5-3d1e-4e57-a02c-4ad3c2a3b8a1"})
	if _, warning, err := l.LoadBase("arm64"); err != nil || !strings.Contains(warning, "kota build") {
		t.Errorf("stale disk: warning %q, %v", warning, err)
	}
	os.Remove(filepath.Join(dir, BaseKernel))
	if _, _, err := l.LoadBase("arm64"); err == nil {
		t.Error("loaded a disk without its kernel")
	}
}

func TestLayoutPerProject(t *testing.T) {
	l := Layout{Root: "/c/engine"}
	a, b := l.ArchiveDir("/p/a"), l.ArchiveDir("/p/b")
	if a == b || !strings.HasPrefix(a, "/c/engine/projects/") {
		t.Errorf("archive dirs %s, %s", a, b)
	}
}

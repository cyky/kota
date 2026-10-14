package gatewaykey

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("good", "sk-from-file\n")
	empty := write("empty", " \n")
	spaced := write("spaced", "sk bad")
	headers := write("headers", "sk\r\nX-Evil: 1")

	cases := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr string
	}{
		{name: "none", env: nil, want: ""},
		{name: "env", env: map[string]string{EnvKey: "sk-env"}, want: "sk-env"},
		{name: "file", env: map[string]string{EnvFile: good}, want: "sk-from-file"},
		{name: "both", env: map[string]string{EnvKey: "sk-env", EnvFile: good}, wantErr: "both"},
		{name: "missing file", env: map[string]string{EnvFile: filepath.Join(dir, "nope")}, wantErr: "no such file"},
		{name: "empty file", env: map[string]string{EnvFile: empty}, wantErr: "empty"},
		{name: "space in file", env: map[string]string{EnvFile: spaced}, wantErr: "printable"},
		{name: "header injection", env: map[string]string{EnvFile: headers}, wantErr: "printable"},
		{name: "newline in env", env: map[string]string{EnvKey: "sk\nX: y"}, wantErr: "printable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Load(func(k string) string { return c.env[k] })
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				// Errors may be printed; they must not contain the key.
				for _, secret := range []string{"sk-env", "sk-from-file", "sk bad", "X-Evil", "X: y"} {
					if strings.Contains(err.Error(), secret) {
						t.Errorf("error %q contains the key", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("key = %q, want %q", got, c.want)
			}
		})
	}
}

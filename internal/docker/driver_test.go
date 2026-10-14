package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoverProject(t *testing.T) {
	for _, c := range []struct {
		name, ids, psExit, rmExit string
		wantRemove, wantErr       bool
	}{
		{name: "container gone"},
		{name: "orphaned containers", ids: "abc123\ndef456\n", wantRemove: true},
		{name: "Docker unavailable", psExit: "1", wantErr: true},
		{name: "removal fails", ids: "abc123\n", rmExit: "1", wantRemove: true, wantErr: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			bin := t.TempDir()
			log := filepath.Join(bin, "calls")
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$TEST_DOCKER_LOG"
case "$1" in
  ps) printf '%s' "$TEST_DOCKER_IDS"; exit "${TEST_DOCKER_PS_EXIT:-0}" ;;
  rm) exit "${TEST_DOCKER_RM_EXIT:-0}" ;;
  *) exit 2 ;;
esac
`
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("TEST_DOCKER_LOG", log)
			t.Setenv("TEST_DOCKER_IDS", c.ids)
			t.Setenv("TEST_DOCKER_PS_EXIT", c.psExit)
			t.Setenv("TEST_DOCKER_RM_EXIT", c.rmExit)
			if err := RecoverProject("localllm-project-a"); (err != nil) != c.wantErr {
				t.Fatalf("recover = %v, want error %v", err, c.wantErr)
			}
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			calls := string(data)
			if !strings.HasPrefix(calls, "ps -aq --filter label=com.docker.compose.project=localllm-project-a\n") {
				t.Errorf("recovery isn't scoped to this compose project: %q", calls)
			}
			if strings.Contains(calls, "rm -f ") != c.wantRemove {
				t.Errorf("container removal = %q, want remove %v", calls, c.wantRemove)
			}
			if c.wantRemove && !strings.Contains(calls, "rm -f "+strings.Join(strings.Fields(c.ids), " ")+"\n") {
				t.Errorf("wrong containers removed: %q", calls)
			}
		})
	}
}

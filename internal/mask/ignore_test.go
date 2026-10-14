package mask

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type ruleCase struct {
	name  string
	rules []string
	path  string
	dir   bool
	want  bool
}

var ruleCases = []ruleCase{
	{"basename at root", []string{".env"}, ".env", false, true},
	{"basename at depth", []string{".env"}, "app/config/.env", false, true},
	{"basename is not a suffix", []string{".env"}, "app/x.env", false, false},
	{"star", []string{"*.pem"}, "certs/server.pem", false, true},
	{"star stays in its segment", []string{"a*b"}, "a/b", false, false},
	{"question mark", []string{"key?.txt"}, "key1.txt", false, true},
	{"class", []string{"key[0-9]"}, "key7", false, true},
	{"negated class", []string{"key[!0-9]"}, "key7", false, false},
	{"negated class matches", []string{"key[!0-9]"}, "keyx", false, true},
	{"dir rule matches dir", []string{"secrets/"}, "app/secrets", true, true},
	{"dir rule skips file", []string{"secrets/"}, "app/secrets", false, false},
	{"leading slash anchors", []string{"/config.yml"}, "sub/config.yml", false, false},
	{"leading slash at root", []string{"/config.yml"}, "config.yml", false, true},
	{"middle slash anchors", []string{"config/prod.yml"}, "config/prod.yml", false, true},
	{"middle slash not at depth", []string{"config/prod.yml"}, "app/config/prod.yml", false, false},
	{"leading double star", []string{"**/creds.json"}, "a/b/creds.json", false, true},
	{"leading double star at root", []string{"**/creds.json"}, "creds.json", false, true},
	{"middle double star", []string{"a/**/key"}, "a/x/y/key", false, true},
	{"middle double star zero dirs", []string{"a/**/key"}, "a/key", false, true},
	{"trailing double star", []string{"vault/**"}, "vault/a/b", false, true},
	{"trailing double star covers the dir", []string{"vault/**"}, "vault", true, true},
	{"trailing double star not a file", []string{"vault/**"}, "vault", false, false},
	{"dir rule with star", []string{"*.d/"}, "conf.d", true, true},
	{"negated dir", []string{"secrets/", "!keep/secrets/"}, "keep/secrets", true, false},
	{"negation", []string{".env.*", "!.env.example"}, ".env.example", false, false},
	{"negation then re-exclude", []string{".env.*", "!.env.example", ".env.example"}, ".env.example", false, true},
	{"negation only", []string{"!.env"}, ".env", false, false},
	{"comment", []string{"# .env"}, "# .env", false, false},
	{"escaped hash", []string{`\#notes`}, "#notes", false, true},
	{"escaped bang", []string{`\!important`}, "!important", false, true},
	{"trailing spaces trimmed", []string{".env   "}, ".env", false, true},
	{"escaped trailing space", []string{`a\ `}, "a ", false, true},
	{"dots are literal", []string{"a.b"}, "axb", false, false},
	{"regexp chars are literal", []string{"a+(b)"}, "a+(b)", false, true},
	{"crlf", []string{".env\r"}, ".env", false, true},
}

func TestRules(t *testing.T) {
	for _, tt := range ruleCases {
		t.Run(tt.name, func(t *testing.T) {
			rs, err := parseRules(tt.rules, "test")
			if err != nil {
				t.Fatal(err)
			}
			if got := rs.excluded(tt.path, tt.dir); got != tt.want {
				t.Errorf("excluded(%q, dir=%v) with %q = %v, want %v", tt.path, tt.dir, tt.rules, got, tt.want)
			}
		})
	}
}

func TestDefaultRules(t *testing.T) {
	rs, err := parseRules(DefaultRules, "defaults")
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{
		".env":                true,
		"api/.env.production": true,
		".env.example":        false,
		".env.local":          true,
		"certs/tls.key":       true,
		"id_rsa":              true,
		"id_ed25519":          true,
		"id_ed25519.pub":      false,
		".npmrc":              true,
		"terraform.tfstate":   true,
		"main.go":             false,
		"README.md":           false,
		"envoy.yaml":          false,
	} {
		if got := rs.excluded(path, false); got != want {
			t.Errorf("%s: excluded = %v, want %v", path, got, want)
		}
	}
	if !rs.excluded("deploy/.aws", true) {
		t.Error(".aws directory not excluded")
	}
}

// TestRulesMatchGit checks each case against git's own matcher.
func TestRulesMatchGit(t *testing.T) {
	isolateGit(t)
	for _, tt := range ruleCases {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			git(t, dir, "init", "-q")
			write(t, filepath.Join(dir, ".gitignore"), strings.Join(tt.rules, "\n")+"\n")
			path := tt.path
			if tt.dir {
				if err := os.MkdirAll(filepath.Join(dir, path), 0o755); err != nil {
					t.Fatal(err)
				}
				path += "/"
			}
			err := exec.Command("git", "-C", dir, "check-ignore", "-q", "--no-index", path).Run()
			gitSays := err == nil
			if gitSays != tt.want {
				t.Errorf("git says excluded(%q, dir=%v) with %q = %v, test wants %v", tt.path, tt.dir, tt.rules, gitSays, tt.want)
			}
		})
	}
}

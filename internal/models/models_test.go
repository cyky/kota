package models

import (
	"slices"
	"strings"
	"testing"

	"kota/assets"
)

const testFile = `
default: coder
small: tiny
unknown:
  context: 8192
  output: 2048
  tools: true
models:
  coder:
    context: 129024
    output: 16384
    tools: true
  tiny:
    context: 16384
  completion:
    tools: false
    agent: false
`

func parse(t *testing.T, data string) File {
	t.Helper()
	f, err := ParseFile([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestResolve(t *testing.T) {
	f := parse(t, testFile)
	coder := Model{ID: "coder", Context: 129024, Output: 16384, Tools: true}
	tiny := Model{ID: "tiny", Context: 16384, Output: 2048, Tools: true}
	fresh := Model{ID: "fresh", Context: 8192, Output: 2048, Tools: true}

	cases := []struct {
		name       string
		discovered []string
		override   string
		want       Catalog
		wantErr    []string
	}{
		{
			name:       "merge and hide",
			discovered: []string{"tiny", "completion", "coder"},
			want:       Catalog{Models: []Model{coder, tiny}, Default: "coder", Small: "tiny"},
		},
		{
			name:       "unknown model gets defaults",
			discovered: []string{"coder", "fresh", "tiny"},
			want:       Catalog{Models: []Model{coder, fresh, tiny}, Default: "coder", Small: "tiny"},
		},
		{
			name:       "duplicates collapse",
			discovered: []string{"coder", "coder", "tiny"},
			want:       Catalog{Models: []Model{coder, tiny}, Default: "coder", Small: "tiny"},
		},
		{
			name:       "override",
			discovered: []string{"coder", "fresh", "tiny"},
			override:   "fresh",
			want:       Catalog{Models: []Model{coder, fresh, tiny}, Default: "fresh", Small: "tiny"},
		},
		{
			name:       "small model missing falls back with a note",
			discovered: []string{"coder"},
			want: Catalog{Models: []Model{coder}, Default: "coder",
				Notes: []string{`small model "tiny" is not available; titles and summaries use "coder"`}},
		},
		{
			name:       "unknown override",
			discovered: []string{"coder", "tiny"},
			override:   "nope",
			wantErr:    []string{`model "nope" is not served`, "available: coder, tiny"},
		},
		{
			name:       "hidden override",
			discovered: []string{"coder", "completion", "tiny"},
			override:   "completion",
			wantErr:    []string{`"completion" is hidden from agents`, "available: coder, tiny"},
		},
		{
			name:       "default not served",
			discovered: []string{"fresh", "tiny"},
			wantErr:    []string{`default model "coder" from models.yaml is not served`, "available: fresh, tiny"},
		},
		{
			name:       "nothing for agents",
			discovered: []string{"completion"},
			wantErr:    []string{"no models for agents"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Resolve(c.discovered, f, c.override)
			if c.wantErr != nil {
				if err == nil {
					t.Fatalf("no error, got %+v", got)
				}
				for _, s := range c.wantErr {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q lacks %q", err, s)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.Models, c.want.Models) || got.Default != c.want.Default ||
				got.Small != c.want.Small || !slices.Equal(got.Notes, c.want.Notes) {
				t.Errorf("Resolve\n got %+v\nwant %+v", got, c.want)
			}
		})
	}
}

func TestParseFileErrors(t *testing.T) {
	cases := map[string]string{
		"typo":            strings.Replace(testFile, "context: 129024", "contxt: 129024", 1),
		"no default":      strings.Replace(testFile, "default: coder", "", 1),
		"partial unknown": strings.Replace(testFile, "  tools: true\nmodels", "models", 1),
		"zero context":    strings.Replace(testFile, "context: 16384", "context: 0", 1),
		"not yaml":        "default: [",
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseFile([]byte(data)); err == nil {
				t.Error("no error")
			}
		})
	}
}

func TestParseDiscovered(t *testing.T) {
	ids, err := ParseDiscovered([]byte(`{"object":"list","data":[{"id":"a","object":"model"},{"id":""},{"id":"b"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids, []string{"a", "b"}) {
		t.Errorf("ids = %v", ids)
	}
	if _, err := ParseDiscovered([]byte("<html>")); err == nil {
		t.Error("no error for a non-JSON response")
	}
}

// TestShippedFile checks the repo's models.yaml against what llama-swap
// serves today.
func TestShippedFile(t *testing.T) {
	f, err := ParseFile(assets.Models)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Resolve([]string{"qwen3.8-27b", "qwen3.6-coder", "qwen3.5-2b-instruct", "zeta2.1"}, f, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Default != "qwen3.6-coder" || c.Small != "qwen3.5-2b-instruct" {
		t.Errorf("default = %q, small = %q", c.Default, c.Small)
	}
	for _, m := range c.Models {
		if m.ID == "zeta2.1" {
			t.Error("zeta2.1 is offered to agents")
		}
	}
}

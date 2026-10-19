package approval

import (
	"bytes"
	"io"
	"strconv"
	"strings"
	"testing"

	"kota/internal/offlinegit"
)

type Status = offlinegit.Status

func TestNeedsConfirmation(t *testing.T) {
	dirty := Status{Git: true, Changes: []string{" M main.go"}}
	clean := Status{Git: true}
	cases := []struct {
		name     string
		fullAuto bool
		status   Status
		want     bool
	}{
		{"full auto, dirty", true, dirty, true},
		{"full auto, clean", true, clean, false},
		{"full auto, not git", true, Status{}, false},
		{"ask mode, dirty", false, dirty, false},
		{"ask mode, clean", false, clean, false},
	}
	for _, c := range cases {
		if got := NeedsConfirmation(c.fullAuto, c.status); got != c.want {
			t.Errorf("%s: NeedsConfirmation = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestConfirm(t *testing.T) {
	s := Status{Git: true, Changes: []string{" M main.go", "?? new.txt"}}
	cases := map[string]bool{
		"y\n":   true,
		"Y\n":   true,
		" y \n": true,
		"y":     true,
		"yes\n": false,
		"n\n":   false,
		"\n":    false,
		"":      false,
	}
	for input, want := range cases {
		var out bytes.Buffer
		if got := Confirm(strings.NewReader(input), &out, "the agent", s); got != want {
			t.Errorf("Confirm(%q) = %v, want %v", input, got, want)
		}
		for _, line := range []string{"the agent runs in full auto", "2 uncommitted change(s)", " M main.go", "?? new.txt", "Type y"} {
			if !strings.Contains(out.String(), line) {
				t.Errorf("prompt lacks %q:\n%s", line, out.String())
			}
		}
	}
}

func TestConfirmTruncatesLongLists(t *testing.T) {
	var s Status
	for i := range 25 {
		s.Changes = append(s.Changes, "?? f"+strconv.Itoa(i))
	}
	var out bytes.Buffer
	Confirm(strings.NewReader("n\n"), &out, "the agent", s)
	if strings.Contains(out.String(), "f10") || !strings.Contains(out.String(), "and 15 more") {
		t.Errorf("prompt:\n%s", out.String())
	}
}

func TestConfirmLeavesTheRestOfInput(t *testing.T) {
	in := strings.NewReader("y\nfor the agent\n")
	if !Confirm(in, &bytes.Buffer{}, "the agent", Status{Git: true, Changes: []string{"?? x"}}) {
		t.Fatal("declined")
	}
	rest := new(strings.Builder)
	if _, err := io.Copy(rest, in); err != nil || rest.String() != "for the agent\n" {
		t.Errorf("rest of input = %q, %v", rest.String(), err)
	}
}

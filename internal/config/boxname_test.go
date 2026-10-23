package config

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

func host(name string, err error) func() (string, error) {
	return func() (string, error) { return name, err }
}

func env(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestBoxNameSuggestsTheHostname(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		hostname func() (string, error)
		want     string
	}{
		{"hostname", nil, host("kitchen-pi", nil), "kitchen-pi"},
		{"trimmed", nil, host("  kitchen-pi\n", nil), "kitchen-pi"},
		{"generic names are still offered", nil, host("raspberrypi", nil), "raspberrypi"},
		{"empty is withheld", nil, host("", nil), ""},
		{"blank is withheld", nil, host("   ", nil), ""},
		{"localhost is withheld", nil, host("localhost", nil), ""},
		{"LOCALHOST is withheld", nil, host("LocalHost", nil), ""},
		{"an unreadable hostname is withheld", nil, host("ignored", errors.New("no")), ""},
		{"no hostname source", nil, nil, ""},
		{"BOX_NAME overrides", map[string]string{"BOX_NAME": " Cabin "}, host("raspberrypi", nil), "Cabin"},
		{"blank BOX_NAME falls back", map[string]string{"BOX_NAME": "  "}, host("kitchen-pi", nil), "kitchen-pi"},
		{"BOX_NAME=localhost is withheld", map[string]string{"BOX_NAME": "localhost"}, host("kitchen-pi", nil), ""},
		{"control characters dropped", map[string]string{"BOX_NAME": "Barn\x00\tPhone\x7f"}, nil, "BarnPhone"},
		{"invalid UTF-8 dropped", map[string]string{"BOX_NAME": "Barn\xff"}, nil, "Barn"},
	}
	for _, c := range cases {
		if got := BoxName(env(c.env), c.hostname); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestBoxNameTruncatesOnARuneBoundary(t *testing.T) {
	long := strings.Repeat("a", 100)
	if got := BoxName(env(nil), host(long, nil)); got != strings.Repeat("a", MaxBoxName) {
		t.Fatalf("ASCII: got %d characters", len(got))
	}
	exact := strings.Repeat("b", MaxBoxName)
	if got := BoxName(env(nil), host(exact, nil)); got != exact {
		t.Fatalf("exactly %d characters must be kept whole, got %q", MaxBoxName, got)
	}
	// 63 ASCII then a two-unit emoji: the emoji does not fit and is not split.
	emoji := strings.Repeat("c", MaxBoxName-1) + "\U0001F4DE" + "dd"
	got := BoxName(env(map[string]string{"BOX_NAME": emoji}), nil)
	if got != strings.Repeat("c", MaxBoxName-1) || !utf8.ValidString(got) {
		t.Fatalf("emoji cut mid-character: %q", got)
	}
	// Multi-byte characters count once each, as the wire schema counts them.
	accented := strings.Repeat("é", 70)
	got = BoxName(env(map[string]string{"BOX_NAME": accented}), nil)
	if n := len(utf16.Encode([]rune(got))); n != MaxBoxName || !utf8.ValidString(got) {
		t.Fatalf("accented: %d units, valid %v", n, utf8.ValidString(got))
	}
	// A cut that lands after a space does not leave it trailing.
	spaced := strings.Repeat("e", MaxBoxName-1) + " fgh"
	if got := BoxName(env(map[string]string{"BOX_NAME": spaced}), nil); got != strings.Repeat("e", MaxBoxName-1) {
		t.Fatalf("trailing space kept: %q", got)
	}
}

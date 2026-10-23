package config

import (
	"strings"
	"unicode"
	"unicode/utf16"
)

// MaxBoxName is the longest name the box suggests, counted the way the sync
// wire schema counts a string's length (UTF-16 code units, as JavaScript
// does), so a suggestion the box sends always fits the field it lands in.
const MaxBoxName = 64

// BoxName is the name the box suggests to its connected account: BOX_NAME
// when set, otherwise the hostname. It is a starting point the household can
// rename, so a generic hostname is still offered; only an empty name and
// "localhost", which name no box in particular, are withheld (""). Read
// without requiring the rest of the environment to be valid.
func BoxName(get func(string) string, hostname func() (string, error)) string {
	name := strings.TrimSpace(get("BOX_NAME"))
	if name == "" && hostname != nil {
		if h, err := hostname(); err == nil {
			name = h
		}
	}
	return suggestedName(name)
}

func suggestedName(name string) string {
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == unicode.ReplacementChar || unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(name, "")))
	// Cut on a rune boundary, never mid-character, then trim again so a cut
	// does not leave a trailing space.
	units := 0
	for i, r := range name {
		units += utf16.RuneLen(r)
		if units > MaxBoxName {
			name = strings.TrimSpace(name[:i])
			break
		}
	}
	if name == "" || strings.EqualFold(name, "localhost") {
		return ""
	}
	return name
}

package policy

import (
	"regexp"
	"strings"
)

// A phone's hardware address is written every way a label, a router and a
// human can manage — colons, dashes, dots, bare hex, any case. The inventory
// accepts all of them and works in one canonical form, because the MAC is
// what names the phone's configuration file and a file that is almost the
// right name serves nobody.
var (
	macSeparators    = strings.NewReplacer(":", "", "-", "", ".", "", " ", "")
	macHexPattern    = regexp.MustCompile(`^[0-9a-f]{12}$`)
	modelPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	phonebookPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
)

// NormaliseMAC returns the canonical aa:bb:cc:dd:ee:ff form of a MAC address
// written in any common style, and false when the input is not one.
func NormaliseMAC(s string) (string, bool) {
	hex := strings.ToLower(macSeparators.Replace(strings.TrimSpace(s)))
	if !macHexPattern.MatchString(hex) {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(hex[i : i+2])
	}
	return b.String(), true
}

// MACFilename is the MAC as a provisioning filename wants it: twelve hex
// digits, no separators, lower case — cfg<this>.xml on a Grandstream.
func MACFilename(canonical string) string {
	return strings.ReplaceAll(canonical, ":", "")
}

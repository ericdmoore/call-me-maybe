package policy

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// Rotation is the result of RotatePins: which extensions changed and what
// their new PINs are. The new PINs exist only here and on disk — they are for
// the operator's terminal, never for logs.
type Rotation struct {
	Path    string
	Changes []PinChange
}

type PinChange struct {
	Label  string
	NewPIN string
}

// RotatePins replaces the PIN of every extension whose label matches one of
// labels (case-insensitive), or of every extension when labels is empty, with
// a freshly generated random PIN of the same length. Disabled extensions
// rotate too — a disabled extension is still a credential.
//
// The file is edited surgically — only the `pin = "..."` values change, so
// comments and formatting survive — validated before writing, and written
// atomically with the original file mode. If anything is wrong, the file on
// disk is untouched.
func RotatePins(path string, labels []string) (*Rotation, error) {
	return RotatePinsSplit(path, "", labels)
}

// RotatePinsSplit is RotatePins for the two-file layout: PINs live in and
// are rewritten in the policy file; the handsets file is read only so
// validation can resolve cross-references.
func RotatePinsSplit(path, handsetsPath string, labels []string) (*Rotation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var hdata []byte
	if handsetsPath != "" {
		hdata, err = os.ReadFile(handsetsPath)
		if os.IsNotExist(err) {
			hdata = nil
		} else if err != nil {
			return nil, err
		}
	}

	// The starting point must itself be valid; rotating a broken file would
	// hide the breakage behind a fresh mtime.
	if _, err := fromSplitTOML(data, hdata); err != nil {
		return nil, fmt.Errorf("refusing to rotate an invalid policy: %w", err)
	}

	var f File
	if _, err := toml.Decode(string(data), &f); err != nil {
		return nil, err
	}

	want := make(map[string]bool, len(labels))
	for _, l := range labels {
		want[strings.ToLower(l)] = true
	}

	rotateAll := len(want) == 0
	var selected []Extension
	for _, e := range f.Extensions {
		if rotateAll || want[strings.ToLower(e.Label)] {
			selected = append(selected, e)
			delete(want, strings.ToLower(e.Label))
		}
	}
	if len(want) > 0 {
		var missing []string
		for l := range want {
			missing = append(missing, l)
		}
		return nil, fmt.Errorf("no extension labelled %q", strings.Join(missing, ", "))
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no extensions to rotate")
	}

	// All pins, old and new, so a fresh pin can never collide with any other.
	taken := make(map[string]bool, len(f.Extensions))
	for _, e := range f.Extensions {
		taken[e.PIN] = true
	}

	text := string(data)
	rotation := &Rotation{Path: path}
	for _, e := range selected {
		fresh, err := generatePIN(len(e.PIN), taken)
		if err != nil {
			return nil, err
		}
		taken[fresh] = true

		// Anchor to line start so a pin quoted in a comment ("# was 428917")
		// can never be edited by mistake. Duplicate pins are a validation
		// error, so exactly one line must match.
		pattern := regexp.MustCompile(`(?m)^(\s*)pin(\s*=\s*)"` + regexp.QuoteMeta(e.PIN) + `"`)
		matches := pattern.FindAllStringIndex(text, -1)
		if len(matches) != 1 {
			return nil, fmt.Errorf("expected exactly one pin line for %q, found %d", e.Label, len(matches))
		}
		text = pattern.ReplaceAllString(text, `${1}pin${2}"`+fresh+`"`)

		rotation.Changes = append(rotation.Changes, PinChange{Label: e.Label, NewPIN: fresh})
	}

	// The edited file must compile before it may touch the disk.
	if _, err := fromSplitTOML([]byte(text), hdata); err != nil {
		return nil, fmt.Errorf("rotation produced an invalid policy, aborting: %w", err)
	}

	if err := writeAtomic(path, []byte(text)); err != nil {
		return nil, err
	}
	return rotation, nil
}

// generatePIN returns a uniformly random digit string of the given length
// that is not already taken. crypto/rand, not math/rand — extensions are
// credentials exposed to the PSTN.
func generatePIN(length int, taken map[string]bool) (string, error) {
	if length < 4 {
		length = 6
	}
	ten := big.NewInt(10)
	for range 1000 {
		var b strings.Builder
		for range length {
			n, err := rand.Int(rand.Reader, ten)
			if err != nil {
				return "", err
			}
			b.WriteByte(byte('0' + n.Int64()))
		}
		pin := b.String()
		if !taken[pin] {
			return pin, nil
		}
	}
	return "", fmt.Errorf("could not generate a unique %d-digit pin", length)
}

// writeAtomic writes via a temp file in the same directory plus rename, so a
// crash mid-write can never leave a half-written policy — and so the hot
// reloader observes exactly one change. The original file mode is preserved
// (policy.toml should be 0600; rotation must not loosen that).
func writeAtomic(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".policy-rotate-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(info.Mode()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

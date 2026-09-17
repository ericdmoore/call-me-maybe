// Package xdg resolves doorman's configuration and state homes consistently.
package xdg

import "path/filepath"

// Dir returns an XDG home, falling back to the corresponding directory in home.
// An unavailable home is left empty rather than writing into the working directory.
func Dir(kind string, getenv func(string) string, home func() (string, error)) string {
	if p := getenv("XDG_" + kind + "_HOME"); p != "" {
		return p
	}
	p, err := home()
	if err != nil || p == "" {
		return ""
	}
	if kind == "STATE" {
		return filepath.Join(p, ".local", "state")
	}
	return filepath.Join(p, ".config")
}

package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// RotateSecrets replaces the value of each named variable in an env file
// with a fresh secret from gen, adding any that are missing, and writes the
// result atomically beside a timestamped backup. The new values are never
// returned: nothing that calls this needs to see a SIP password, and the
// phone that does fetches it through `doorman provision`.
func RotateSecrets(envPath string, keys []string, gen func() (string, error)) error {
	raw, err := os.ReadFile(envPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", envPath, err)
	}
	out := string(raw)
	for _, key := range keys {
		if !envKeyPattern.MatchString(key) {
			return fmt.Errorf("%q is not an environment variable name", key)
		}
		value, err := gen()
		if err != nil {
			return err
		}
		re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `=.*$`)
		line := key + "=" + value
		if re.MatchString(out) {
			out = re.ReplaceAllLiteralString(out, line)
		} else {
			out = strings.TrimRight(out, "\n") + "\n" + line + "\n"
		}
	}
	tmp := filepath.Join(filepath.Dir(envPath), "."+filepath.Base(envPath)+".rotate.tmp")
	if err := os.WriteFile(tmp, []byte(out), 0o600); err != nil {
		return err
	}
	if _, err := Backup(envPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, envPath)
}

var envKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

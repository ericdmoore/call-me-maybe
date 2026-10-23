// Package configcheck is the one answer to "is this configuration valid?".
//
// `doorman check`, the daemon's policy reload, the sync observer and the
// hosted validator built from this source all take their rules from here, so
// they accept and refuse the same files (call-me-maybe#131). Before, check and
// the daemon accepted a disabled schedule starting at 25:99 that sync refused
// without saying why.
//
// The rules themselves live in internal/policy; this package exists because
// the model registry they need is internal/provision, which imports policy.
package configcheck

import (
	"callmemaybe/internal/policy"
	"callmemaybe/internal/provision"
)

// Options is the shared rule set: unknown keys are errors, a handset model
// must be one doorman knows, and [line] trunk is checked against the trunk
// inventory. trunks nil means there is no trunks.toml, which is itself
// something a line naming a trunk is told about.
func Options(trunks *policy.Trunks) policy.Options {
	if trunks == nil {
		trunks = &policy.Trunks{}
	}
	return policy.Options{StrictUnknownKeys: true, ModelKnown: provision.Known, Trunks: trunks}
}

// Problems validates one line's policy file with the shared inventory and
// returns every problem, or none when the files are valid. handsetsData nil
// is the legacy single-file layout; trunksData nil means no trunks.toml. The
// messages may quote values from the files; pass them through policy.Redact-
// Problem before they reach a log or leave the box.
func Problems(policyData, handsetsData, trunksData []byte) []string {
	var trunks *policy.Trunks
	if trunksData != nil {
		t, err := policy.TrunksFromTOML(trunksData)
		if err != nil {
			return []string{"trunks: " + err.Error()}
		}
		trunks = t
	}
	return policy.LintSplitWith(policyData, handsetsData, Options(trunks))
}

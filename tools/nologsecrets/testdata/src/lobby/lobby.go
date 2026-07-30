// Package lobby exercises nologsecrets against the shapes that actually
// appear in internal/lobby.
package lobby

import (
	"fmt"
	"log/slog"
)

type resolved struct {
	PIN   string
	Label string
}

type session struct {
	log        *slog.Logger
	callerE164 string
	callerRaw  string
}

func tail(s string) string { return s[len(s)-4:] }

func (s *session) bad(digits string, ext resolved, attempts int) {
	s.log.Info("entry", "digits", digits)                      // want `digits reaches the logs.*`
	s.log.Warn("wrong pin", "pin", ext.PIN)                    // want `ext.PIN reaches the logs.*`
	s.log.Info("caller", "from", s.callerE164)                 // want `s.callerE164 reaches the logs.*`
	s.log.Error("raw", "n", s.callerRaw)                       // want `s.callerRaw reaches the logs.*`
	s.log.Info("fmt does not redact", "d", fmt.Sprint(digits)) // want `digits reaches the logs.*`
	s.log.Info("slicing is not redaction", "d", digits[:2])    // want `digits reaches the logs.*`
	s.log.Info("attempts are fine", "attempt", attempts)
}

// good covers the values the daemon legitimately logs today.
func (s *session) good(digits string, ext resolved, stage int) {
	s.log.Info("dial window elapsed", "digits", len(digits))
	s.log.Info("extension accepted", "extension", ext.Label)
	s.log.Warn("invalid extension", "attempt", stage)
	s.log.Info("last four only", "caller", tail(s.callerE164))
	s.log.Info("no arguments at all")
}

// Regression: names containing a sensitive substring but holding a count.
// The first version of this analyzer flagged pol.PinLength in main.go, which
// is the integer 6 — how long PINs are, not what any of them is.
type policy struct {
	PinLength      int
	MaxPinAttempts int
}

func (s *session) counts(pol policy, enteredDigits int) {
	s.log.Info("policy loaded", "pinLength", pol.PinLength)
	s.log.Info("limits", "maxPinAttempts", pol.MaxPinAttempts)
	s.log.Info("how many arrived", "digitCount", enteredDigits)
}

// notALogger must be ignored: same method name, different type.
type notALogger struct{}

func (notALogger) Info(msg string, args ...any) {}

func alsoFine(pin string) {
	var n notALogger
	n.Info("not slog", "pin", pin)
}

// Regression: attributes bound with With ride on every subsequent line from
// that logger, so this leaks more than a single Info call would. The analyzer
// originally ignored With entirely, which is how a full E.164 ended up attached
// to every session log line in the real daemon.

func redactCaller(e164, raw string, allowFull bool) string { return e164 }

func bindLogger(base *slog.Logger, callerE164, callerRaw string) *slog.Logger {
	_ = base.With("callId", "abc", "caller", callerE164) // want `callerE164 reaches the logs.*`
	_ = base.With("caller", callerRaw)                   // want `callerRaw reaches the logs.*`

	// The only acceptable form: routed through a redactor.
	return base.With("callId", "abc", "caller", redactCaller(callerE164, callerRaw, false))
}

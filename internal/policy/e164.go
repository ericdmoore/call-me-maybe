package policy

import "strings"

// Caller ID normalisation.
//
// VoIP.ms passes the calling number through more or less as the originating
// carrier hands it over, which in practice means all of these arrive for the
// same human being:
//
//	5125550100          bare national
//	15125550100         national with country code, no plus
//	+15125550100        proper E.164
//	(512) 555-0100      formatted, rare but it happens
//
// If raw strings were compared against the allow-list, Grandma would get the
// bouncer roughly a third of the time. So everything folds to E.164 first.
//
// This is deliberately not a full libphonenumber. It handles NANP correctly
// and passes through anything already in E.164 form. If real global parsing
// is ever needed, swap the body of NormaliseCallerID — call sites don't change.

// CallerKind classifies what a raw caller ID string turned out to be.
type CallerKind int

const (
	// KindE164 is a usable number, normalised to +<digits>.
	KindE164 CallerKind = iota
	// KindAnonymous is withheld/absent caller ID. Always meets the bouncer.
	KindAnonymous
	// KindUnparseable is short codes and junk. Never allow-listed.
	KindUnparseable
)

// NormalisedCallerID is the result of folding a raw caller ID string.
type NormalisedCallerID struct {
	Kind  CallerKind
	Value string // populated only when Kind == KindE164
	Raw   string
}

// Values Asterisk uses when there is no usable caller ID.
var anonymousValues = map[string]bool{
	"":            true,
	"anonymous":   true,
	"unknown":     true,
	"restricted":  true,
	"unavailable": true,
	"private":     true,
	"asterisk":    true,
}

// NormaliseCallerID folds a raw caller ID to E.164, or classifies it as
// anonymous/unparseable. defaultCC is the country code assumed for bare
// national numbers ("1" for NANP).
func NormaliseCallerID(raw, defaultCC string) NormalisedCallerID {
	in := strings.TrimSpace(raw)

	if anonymousValues[strings.ToLower(in)] {
		return NormalisedCallerID{Kind: KindAnonymous, Raw: in}
	}

	hadPlus := strings.HasPrefix(in, "+")
	digits := digitsOnly(in)

	if digits == "" {
		return NormalisedCallerID{Kind: KindAnonymous, Raw: in}
	}

	// Already E.164 and plausible.
	if hadPlus && len(digits) >= 8 && len(digits) <= 15 {
		return NormalisedCallerID{Kind: KindE164, Value: "+" + digits, Raw: in}
	}

	cc := digitsOnly(defaultCC)
	if cc == "" {
		cc = "1"
	}

	if cc == "1" {
		// NANP: 10 digits national, or 11 starting with 1.
		if len(digits) == 10 {
			return NormalisedCallerID{Kind: KindE164, Value: "+1" + digits, Raw: in}
		}
		if len(digits) == 11 && digits[0] == '1' {
			return NormalisedCallerID{Kind: KindE164, Value: "+" + digits, Raw: in}
		}
		// 011 is the NANP international dial-out prefix; strip it and treat
		// the remainder as an already-qualified international number.
		if rest, ok := strings.CutPrefix(digits, "011"); ok && len(rest) >= 8 && len(rest) <= 15 {
			return NormalisedCallerID{Kind: KindE164, Value: "+" + rest, Raw: in}
		}
	} else if len(digits) >= 8 && len(digits) <= 15 {
		if strings.HasPrefix(digits, cc) {
			return NormalisedCallerID{Kind: KindE164, Value: "+" + digits, Raw: in}
		}
		return NormalisedCallerID{Kind: KindE164, Value: "+" + cc + digits, Raw: in}
	}

	// Short codes, internal extensions, and genuine junk land here.
	return NormalisedCallerID{Kind: KindUnparseable, Raw: in}
}

// E164OrEmpty returns the normalised E.164 string, or "" if the input is
// anonymous or unparseable — i.e. "" means "send them to the bouncer".
func E164OrEmpty(raw, defaultCC string) string {
	n := NormaliseCallerID(raw, defaultCC)
	if n.Kind == KindE164 {
		return n.Value
	}
	return ""
}

// RedactCaller produces the only caller string that may be attached to a
// logger. It exists so the decision lives in one place rather than at every
// call site: pass the normalised number, the raw one, and whether the operator
// has explicitly opted out of redaction.
//
// The default is to redact. LOG_REDACT_CALLER_ID=false is a deliberate opt-out
// for someone debugging, not a shipping default — see CLAUDE.md invariant 1.
//
// `tools/nologsecrets` recognises this as a narrowing function, so routing a
// caller ID through here is what makes it loggable at all.
func RedactCaller(e164, raw string, allowFull bool) string {
	display := e164
	if display == "" {
		display = raw
	}
	if display == "" {
		return "anonymous"
	}
	if allowFull {
		return display
	}
	if e164 != "" {
		return Redact(e164)
	}
	// A raw value we could not normalise is still a phone number someone dialled
	// from. Redact it on its own terms rather than printing it whole.
	return Redact(raw)
}

// Redact keeps enough of a number to identify a caller in logs without
// printing it: +15125550100 -> +1512•••0100
func Redact(e164 string) string {
	if len(e164) <= 8 {
		return strings.Repeat("•", len(e164))
	}
	return e164[:5] + "•••" + e164[len(e164)-4:]
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

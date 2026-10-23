package policy

import (
	"regexp"
	"strings"
	"unicode/utf16"
)

// MaxProblemLength is the longest validation message that leaves the box in
// sync's status report.
const MaxProblemLength = 300

var (
	problemURL    = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"']+`)
	problemQuoted = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'`)
	// A run of digits as a phone number is written: spaces, dots, dashes,
	// brackets and a leading plus between them.
	problemDigits = regexp.MustCompile(`\+?\(?\d[\d\s().-]*\d|\d`)
	problemToken  = regexp.MustCompile(`[A-Za-z0-9_-]{24,}`)
	problemSpace  = regexp.MustCompile(`\s+`)
)

// RedactProblem turns a validation message into one that is safe for a log
// line or for sync's status report, and at most limit UTF-16 units long (no
// limit when limit <= 0).
//
// Messages quote the offending value by convention, and the value can be a
// phone number, a PIN or a webhook URL holding a token. So: URLs go; a quoted
// value with four digits in a row or seven in a phone-number run goes whole;
// outside quotes, a phone-number run of seven digits goes; long token-like
// strings go. What survives is the field and the shape of the problem —
// `schedule "bedtime" start "25:99" is not a time of day` stays readable, a
// number never does.
func RedactProblem(msg string, limit int) string {
	msg = problemSpace.ReplaceAllString(strings.TrimSpace(msg), " ")
	msg = problemURL.ReplaceAllString(msg, "<url>")
	msg = problemToken.ReplaceAllString(msg, "<redacted>")
	// Quoted values first, so a number inside quotes takes its quotes with it.
	var out strings.Builder
	last := 0
	for _, loc := range problemQuoted.FindAllStringIndex(msg, -1) {
		out.WriteString(redactDigitRuns(msg[last:loc[0]], 7))
		quoted := msg[loc[0]:loc[1]]
		if sensitiveDigits(quoted) {
			quoted = `"<redacted>"`
		}
		out.WriteString(quoted)
		last = loc[1]
	}
	out.WriteString(redactDigitRuns(msg[last:], 7))
	return truncateUTF16(out.String(), limit)
}

// sensitiveDigits reports four digits in a row, or seven in one phone-number
// run: enough to be a PIN or a number, too many to be a time or a count.
func sensitiveDigits(s string) bool {
	for _, run := range problemDigits.FindAllString(s, -1) {
		if digitCount(run) >= 7 {
			return true
		}
		contiguous := 0
		for _, r := range run {
			if r >= '0' && r <= '9' {
				contiguous++
				if contiguous >= 4 {
					return true
				}
			} else {
				contiguous = 0
			}
		}
	}
	return false
}

func redactDigitRuns(s string, min int) string {
	return problemDigits.ReplaceAllStringFunc(s, func(run string) string {
		if digitCount(run) >= min {
			return "<redacted>"
		}
		return run
	})
}

func digitCount(s string) int {
	n := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n++
		}
	}
	return n
}

func truncateUTF16(s string, limit int) string {
	if limit <= 0 || textLength(s) <= limit {
		return s
	}
	n := 0
	for i, r := range s {
		if n+utf16.RuneLen(r) > limit-1 {
			return s[:i] + "…"
		}
		n += utf16.RuneLen(r)
	}
	return s
}

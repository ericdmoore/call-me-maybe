package contacts

import (
	"io"
	"mime/quotedprintable"
	"strings"
)

// vCard parsing: a defined subset, skip-and-count the rest.
//
// This does not implement RFC 2426 or RFC 6350. It accepts what the exporters
// people actually have produce — iCloud, Google Contacts, CardDAV servers, and
// the 2.1 files that still fall out of older phones and Outlook — and counts
// whatever it does not understand so `doorman check` can report it. That is
// the same posture internal/story takes with Markdown, for the same reason:
// accepting exactly what real producers emit beats implementing a spec whose
// long tail we would then have to reject anyway.
//
// What is read:
//
//	BEGIN:VCARD / END:VCARD   card boundaries, any version (2.1, 3.0, 4.0)
//	FN                        the display name; preferred when present
//	N                         family;given;… assembled into a name when FN is absent
//	ORG                       first component only — the organisation
//	TEL                       every one on the card, with its TYPE parameters
//
// What is handled on the way in:
//
//	line folding              a continuation line begins with space or tab
//	QUOTED-PRINTABLE          2.1's encoding, including its trailing-"=" soft breaks
//	TYPE spelled three ways   TYPE=CELL, TYPE=cell, and 2.1's bare CELL
//	quoted parameters         4.0's TYPE="voice,cell"
//	grouped properties        item1.TEL, which iCloud emits constantly
//	tel: URIs                 4.0's VALUE=uri form, with any ;ext= trimmed
//	escapes                   \, \; \n \\ in values, per 3.0/4.0
//
// What is skipped and counted: everything else. Other properties (ADR, EMAIL,
// PHOTO, X-*) are ignored without being counted, because ignoring them is not a
// defect — the parser never wanted them. A line that is not a property at all,
// a nested card, and a card left unterminated at end of file *are* counted, and
// so is every TEL whose value will not normalise to E.164.
//
// CHARSET is accepted and ignored: everything here is treated as UTF-8, which
// is what every exporter in the list above emits. A legacy 2.1 file declaring
// ISO-8859-1 will produce a mangled *name*, never a mangled number — names are
// display only, and admission turns on the number.

// card is one parsed vCard, reduced to the four fields admission turns on.
type card struct {
	name string // FN, or assembled from N
	org  string // first ORG component
	tels []tel
}

// tel is one TEL property: the value as written, and its type tokens folded to
// lower case so TYPE=CELL, TYPE=cell and a bare CELL all read the same.
type tel struct {
	value string
	types []string
}

// skips counts what the parser did not understand. Every field here ends up in
// `doorman check` — silence about a dropped card would be the same defect as
// swallowing an unknown config key.
type skips struct {
	// cards is malformed cards: unterminated at end of file, or nested inside
	// another card (an AGENT property, which no admission decision should turn
	// on).
	cards int
	// lines is content that was not a property line at all.
	lines int
}

// parseCards reads every vCard in a document.
func parseCards(data []byte) ([]card, skips) {
	var (
		cards []card
		cur   *card
		s     skips
		depth int // nesting inside an embedded card, which we deliberately drop
	)

	for _, line := range logicalLines(data) {
		p, ok := parseProperty(line)
		if !ok {
			s.lines++
			continue
		}
		switch p.name {
		case "BEGIN":
			if !strings.EqualFold(p.value, "VCARD") {
				s.lines++
				continue
			}
			if cur != nil {
				depth++
				s.cards++
				continue
			}
			cur = &card{}
			continue
		case "END":
			if !strings.EqualFold(p.value, "VCARD") {
				s.lines++
				continue
			}
			switch {
			case depth > 0:
				depth--
			case cur != nil:
				cards = append(cards, *cur)
				cur = nil
			default:
				s.lines++ // END with no BEGIN
			}
			continue
		}
		if cur == nil {
			s.lines++ // a property loose in the file, outside any card
			continue
		}
		if depth > 0 {
			continue // belongs to the embedded card already counted
		}
		// AGENT:BEGIN:VCARD — 3.0's embedded card, written inline rather than
		// on its own line. Without this the agent's telephone numbers are
		// absorbed by the card that contains them and end up attributed to
		// somebody else, which is the one kind of parse error that produces a
		// wrong admission rather than a missing one.
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(p.value)), "BEGIN:VCARD") {
			depth++
			s.cards++
			continue
		}
		absorb(cur, p)
	}

	// A file truncated mid-card is a file we only half have. Drop the partial
	// card rather than admitting somebody on the strength of it.
	if cur != nil {
		s.cards++
	}
	return cards, s
}

// absorb folds one property into the card being built.
func absorb(c *card, p property) {
	switch p.name {
	case "FN":
		// FN wins over anything N produced: it is the name the address book
		// itself displays.
		if v := strings.TrimSpace(unescape(p.value)); v != "" {
			c.name = v
		}
	case "N":
		if c.name != "" {
			return
		}
		// family;given;additional;prefix;suffix — given then family reads the
		// way a handset display wants it.
		parts := splitEscaped(p.value, ';')
		var family, given string
		if len(parts) > 0 {
			family = strings.TrimSpace(unescape(parts[0]))
		}
		if len(parts) > 1 {
			given = strings.TrimSpace(unescape(parts[1]))
		}
		c.name = strings.TrimSpace(given + " " + family)
	case "ORG":
		// ORG is organisation;department;… and only the first component says
		// which business this is.
		parts := splitEscaped(p.value, ';')
		if len(parts) > 0 {
			if v := strings.TrimSpace(unescape(parts[0])); v != "" {
				c.org = v
			}
		}
	case "TEL":
		c.tels = append(c.tels, tel{value: p.value, types: p.typeTokens()})
	}
}

// ── lines ────────────────────────────────────────────────────────────────

// logicalLines unfolds a document into whole properties.
//
// Two mechanisms, and they are not the same one. RFC 2425 folding continues a
// line with leading whitespace, which every version uses; vCard 2.1 also lets a
// QUOTED-PRINTABLE value continue with a trailing "=" and no leading
// whitespace at all, which is what Outlook and older Nokia exports do to a long
// name. Handling only the first splits those names across two logical lines and
// loses the second half silently.
func logicalLines(data []byte) []string {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	var out []string
	for _, raw := range strings.Split(text, "\n") {
		last := len(out) - 1
		switch {
		case raw == "":
			// Blank lines separate cards in some exporters and mean nothing in
			// any of them.
			continue
		case last >= 0 && (raw[0] == ' ' || raw[0] == '\t'):
			out[last] += raw[1:]
		case last >= 0 && softBreak(out[last]):
			out[last] = out[last][:len(out[last])-1] + raw
		default:
			out = append(out, raw)
		}
	}
	return out
}

// softBreak reports whether a logical line is a quoted-printable value waiting
// for its continuation.
func softBreak(line string) bool {
	if !strings.HasSuffix(line, "=") {
		return false
	}
	name, _, ok := strings.Cut(line, ":")
	if !ok {
		// No colon yet: we are already mid-value on a folded property.
		name = line
	}
	return strings.Contains(strings.ToUpper(name), "QUOTED-PRINTABLE")
}

// ── properties ───────────────────────────────────────────────────────────

// property is one parsed line: NAME, its parameters, and the decoded value.
type property struct {
	name   string
	params []param
	value  string
}

// param is one parameter. 2.1 writes them bare — TEL;WORK;VOICE: — so key is
// empty for those and the token lives in value.
type param struct{ key, value string }

// typeTokens is every TYPE the property carries, lower-cased. Bare 2.1
// parameters count as types once the encoding words are removed, which is what
// makes TEL;CELL: and TEL;TYPE=CELL: the same statement.
func (p property) typeTokens() []string {
	var out []string
	for _, pa := range p.params {
		switch {
		case strings.EqualFold(pa.key, "type"):
		case pa.key == "":
			if notAType[strings.ToLower(pa.value)] {
				continue
			}
		default:
			continue
		}
		for _, t := range strings.Split(pa.value, ",") {
			if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
				out = append(out, t)
			}
		}
	}
	return out
}

// notAType are the bare 2.1 parameters that are encodings rather than kinds of
// telephone. Reading ENCODING as a type would be harmless; reading it as
// evidence of anything would not.
var notAType = map[string]bool{
	"quoted-printable": true,
	"base64":           true,
	"7bit":             true,
	"8bit":             true,
	"uri":              true,
}

// parseProperty splits "[group.]NAME[;param]*:value" and decodes the value.
// Returns false for anything that is not shaped like a property at all.
func parseProperty(line string) (property, bool) {
	head, value, ok := cutOutsideQuotes(line, ':')
	if !ok || head == "" {
		return property{}, false
	}

	fields := splitOutsideQuotes(head, ';')
	name := fields[0]
	// item1.TEL — a property group, which iCloud emits for anything with a
	// custom label. The group has no bearing on what the property means.
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	p := property{name: strings.ToUpper(strings.TrimSpace(name)), value: value}
	if p.name == "" {
		return property{}, false
	}

	for _, f := range fields[1:] {
		k, v, has := strings.Cut(f, "=")
		if !has {
			p.params = append(p.params, param{value: strings.Trim(strings.TrimSpace(k), `"`)})
			continue
		}
		p.params = append(p.params, param{
			key:   strings.TrimSpace(k),
			value: strings.Trim(strings.TrimSpace(v), `"`),
		})
	}

	if p.quotedPrintable() {
		p.value = decodeQP(p.value)
	}
	return p, true
}

func (p property) quotedPrintable() bool {
	for _, pa := range p.params {
		if pa.key == "" || strings.EqualFold(pa.key, "encoding") {
			if strings.EqualFold(pa.value, "quoted-printable") {
				return true
			}
		}
	}
	return false
}

// decodeQP decodes a quoted-printable value, keeping the raw text when it will
// not decode. A name that arrives as "Jos=E9" is worth trying to read; a name
// that arrives as nonsense is still better than an empty one, because the
// number beside it is what admission turns on either way.
func decodeQP(v string) string {
	out, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(v)))
	if err != nil {
		return v
	}
	return string(out)
}

// ── small string surgery ─────────────────────────────────────────────────

// cutOutsideQuotes splits on the first sep that is not inside a quoted
// parameter value. 4.0 writes TYPE="work,voice" and a colon may legally appear
// inside those quotes.
func cutOutsideQuotes(s string, sep byte) (before, after string, found bool) {
	quoted := false
	for i := range len(s) {
		switch s[i] {
		case '"':
			quoted = !quoted
		case sep:
			if !quoted {
				return s[:i], s[i+1:], true
			}
		}
	}
	return s, "", false
}

func splitOutsideQuotes(s string, sep byte) []string {
	var out []string
	quoted, start := false, 0
	for i := range len(s) {
		switch s[i] {
		case '"':
			quoted = !quoted
		case sep:
			if !quoted {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// splitEscaped splits a structured value on an unescaped separator, so
// "O'Neill\;Sons;Plumbing" is two components and not three.
func splitEscaped(s string, sep byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			i++ // whatever follows is literal, separator included
			continue
		}
		if s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// unescape resolves the backslash escapes 3.0 and 4.0 define. 2.1 does not use
// them, and applying them to a 2.1 value is harmless: it has none to resolve.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n', 'N':
			b.WriteByte('\n')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// telNumber reduces a TEL value to something worth handing to E.164
// normalisation: the tel: URI scheme removed, and anything after a ";" dropped.
//
// The trailing part matters more than it looks. "tel:+15125550100;ext=42"
// keeps its digits through a naive strip and normalises to a thirteen-digit
// number belonging to nobody, which would then sit in the set as a real entry.
// Cutting at the ";" leaves a number that either normalises or is counted as
// skipped, and both are honest answers.
func telNumber(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.IndexByte(v, ';'); i >= 0 {
		v = v[:i]
	}
	if rest, ok := cutPrefixFold(v, "tel:"); ok {
		v = rest
	}
	return strings.TrimSpace(v)
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return s, false
}

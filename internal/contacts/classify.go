package contacts

import "strings"

// Classification: is this number published?
//
// The allow-list bypasses the lobby, and caller ID is spoofable — but spoofing
// requires knowing *what* to spoof, and going from six trusted numbers to two
// hundred does not help an attacker, because the numbers they could plausibly
// discover are exactly the ones already on the short list. List size is not the
// risk. Discoverability is.
//
//	If a stranger can look the number up, it must not be automatic admission.
//
// A plumber's number is on their website. A dentist's is in a directory. Your
// sister's mobile is nowhere. The first two are impersonable by anyone with a
// search engine; the third requires knowing you.
//
// The happy accident that makes this implementable with no lookups at all is
// that the vCard already carries the signal: the thing that makes a contact "a
// business" in an address book is the same thing that makes its number
// findable.
//
// # Fail conservative
//
// The asymmetry is stark. Wrong-closed means the plumber hears a ten-second
// greeting and dials an extension. Wrong-open means a findable number rings
// every phone in the house at 3am. So Personal is an allow-list of signals —
// a name, no organisation, and a telephone the card itself calls a mobile or a
// home line — and everything else, including everything ambiguous, is
// Published. An untyped TEL on an unnamed card is not evidence of anything and
// must not be read as evidence of safety.
//
// The classifier never gets the last word. `[[people]]` in policy.toml is the
// deliberate list and beats it outright: if you genuinely want the dentist
// straight through, write them down.

// Class is what a number reads as.
type Class uint8

const (
	// Personal is a number a stranger could not have looked up. These are the
	// ones it is safe to admit without a lobby.
	Personal Class = iota
	// Published is anything discoverable, and anything unclear. The iota order
	// is load-bearing: merging two sources takes the higher class, so the
	// restrictive answer wins a disagreement by construction.
	Published
)

func (c Class) String() string {
	if c == Personal {
		return "personal"
	}
	return "published"
}

// Reason records which signal decided, so `doorman check` can show an operator
// why a contact is in the tier it is in. "Why did the phone ring for someone I
// never allow-listed" — and its mirror, "why does my sister get the lobby" —
// both need an answer that is not "read the source".
type Reason uint8

const (
	// ReasonPersonal is the only way into Personal: a named card, no
	// organisation, and a telephone typed as a mobile or a home line.
	ReasonPersonal Reason = iota
	// ReasonOrg is an ORG on the card. A business, therefore findable.
	ReasonOrg
	// ReasonWork is a TEL typed work, fax, pager or main — published even on a
	// card with no organisation, which is what a colleague's desk line looks
	// like.
	ReasonWork
	// ReasonTollFree is an 800-family number. Never a person's, whatever the
	// card says.
	ReasonTollFree
	// ReasonUnclear is the conservative default: an unnamed card, or a
	// telephone the card declines to characterise.
	ReasonUnclear
)

func (r Reason) String() string {
	switch r {
	case ReasonPersonal:
		return "a person (named, no organisation, mobile or home)"
	case ReasonOrg:
		return "a business (ORG is set)"
	case ReasonWork:
		return "a work number (TYPE=work)"
	case ReasonTollFree:
		return "toll-free"
	default:
		return "unclear — no name, or no telephone type"
	}
}

// personalTypes are the telephone kinds that can be personal. iphone is
// Apple's, mobile is Google's, cell is everyone else's; they mean the same
// thing and all three turn up in real exports.
var personalTypes = map[string]bool{
	"cell":   true,
	"mobile": true,
	"iphone": true,
	"home":   true,
}

// publishedTypes settle a card that has both. A number typed work is published
// even when the card also says home, because the restrictive reading is the
// safe one and someone who works from home has a findable number either way.
var publishedTypes = map[string]bool{
	"work":     true,
	"fax":      true,
	"pager":    true,
	"main":     true,
	"business": true,
	"company":  true,
}

// tollFreeNPA is the North American toll-free set. A toll-free number is never
// somebody's personal line: it is bought to be published, and it is the one
// signal that overrides everything else on the card.
var tollFreeNPA = map[string]bool{
	"800": true, "888": true, "877": true, "866": true,
	"855": true, "844": true, "833": true,
}

// classify decides one number, from one card. e164 is the normalised form; the
// card and the telephone's own type parameters are everything else it gets.
// Nothing here does a lookup of any kind — no network, no directory, no
// heuristic that could give a different answer tomorrow.
func classify(c card, t tel, e164 string) (Class, Reason) {
	if tollFree(e164) {
		return Published, ReasonTollFree
	}
	if c.org != "" {
		return Published, ReasonOrg
	}
	for _, ty := range t.types {
		if publishedTypes[ty] {
			return Published, ReasonWork
		}
	}
	// An unnamed card cannot produce a handset display and cannot be vouched
	// for. It is exactly the shape a bulk import has, and bulk imports are
	// where a findable number gets in unnoticed.
	if c.name == "" {
		return Published, ReasonUnclear
	}
	for _, ty := range t.types {
		if personalTypes[ty] {
			return Personal, ReasonPersonal
		}
	}
	return Published, ReasonUnclear
}

// tollFree reports whether an E.164 number is in the North American toll-free
// range. Deliberately narrow: only +1 numbers of the right length, because
// "the fourth to sixth digits look like 800" is true of plenty of numbers in
// other countries that are nothing of the sort.
func tollFree(e164 string) bool {
	if !strings.HasPrefix(e164, "+1") || len(e164) != 12 {
		return false
	}
	return tollFreeNPA[e164[2:5]]
}

// Package provision holds what doorman knows about the phones themselves:
// which models exist, which of them it can write a configuration file for,
// and (in the template subpackages) what that file says. It is data and pure
// rendering, importable by render, check and the LSP. The LAN-facing serving
// side lives in provision/serve and is reachable only from the provision
// subcommand, by test.
package provision

import (
	"sort"
	"strings"
)

// Model is one kind of phone. Templated says whether doorman can write its
// configuration file today; a known model without a template is still
// worth naming in the inventory — check accepts it, render reports it, and
// the phone keeps the manual path until a template lands.
type Model struct {
	ID        string
	Vendor    string
	Name      string
	Family    string // template family: "grandstream-xml", "yealink-cfg", or "" when none
	Templated bool
	// Discovery records, per model, which address forms a handset has been
	// seen to fetch through in a rehearsal. Empty means only the LAN IP is
	// known to work, which is the form render always prints.
	Discovery []string
}

var models = []Model{
	{ID: "grandstream-wp826", Vendor: "Grandstream", Name: "WP826 Wi-Fi handset", Family: "grandstream-xml", Templated: true},
	{ID: "grandstream-wp816", Vendor: "Grandstream", Name: "WP816 Wi-Fi handset", Family: "grandstream-xml", Templated: true},
	{ID: "grandstream-wp836", Vendor: "Grandstream", Name: "WP836 Wi-Fi handset", Family: "grandstream-xml", Templated: true},
	{ID: "grandstream-grp2601p", Vendor: "Grandstream", Name: "GRP2601P desk phone", Family: "grandstream-xml", Templated: true},
	{ID: "grandstream-grp2602p", Vendor: "Grandstream", Name: "GRP2602P desk phone", Family: "grandstream-xml", Templated: true},
	{ID: "grandstream-grp2602w", Vendor: "Grandstream", Name: "GRP2602W desk phone, Wi-Fi", Family: "grandstream-xml", Templated: true},
	{ID: "grandstream-ht812", Vendor: "Grandstream", Name: "HT812 analog adapter", Family: "grandstream-xml"},
	{ID: "grandstream-dp752", Vendor: "Grandstream", Name: "DP752 DECT base", Family: "grandstream-xml"},
	{ID: "yealink-t31p", Vendor: "Yealink", Name: "T31P desk phone", Family: "yealink-cfg"},
	{ID: "yealink-w70b", Vendor: "Yealink", Name: "W70B DECT base", Family: "yealink-cfg"},
	{ID: "fanvil-x3u", Vendor: "Fanvil", Name: "X3U desk phone"},
	{ID: "cisco-spa504", Vendor: "Cisco", Name: "SPA504 desk phone"},
}

// Models lists every known model, sorted by id.
func Models() []Model {
	out := append([]Model(nil), models...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Lookup finds a model by id.
func Lookup(id string) (Model, bool) {
	for _, m := range models {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

// Known is the shape policy.Options.ModelKnown wants: is this id a model, and
// if not, what did the author probably mean. The suggestion is offered only
// when it is probably right — the same rule the unknown-key diagnostics use —
// so a wild guess never appears in an error message as if it were advice.
func Known(id string) (bool, string) {
	if _, ok := Lookup(id); ok {
		return true, ""
	}
	return false, Suggest(id)
}

// Suggest returns the closest model id, or "" when nothing is close enough.
func Suggest(id string) string {
	id = strings.ToLower(id)
	best, bestDist := "", 4
	for _, m := range models {
		d := editDistance(id, m.ID)
		// A bare model number ("wp826") or a vendor-less form is the common
		// slip; treat a suffix match as a distance of one.
		if strings.HasSuffix(m.ID, "-"+id) || strings.HasSuffix(m.ID, id) {
			d = 1
		}
		if d < bestDist {
			best, bestDist = m.ID, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

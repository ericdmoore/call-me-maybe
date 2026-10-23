package policy

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"unicode/utf16"
)

// Bounds every load path enforces, so that a file the daemon accepts is one
// the sync observer can always represent, and the hosted validator always
// receives. Before these lived here, the observer's wire schema was the only
// place they were checked, and a file could pass `doorman check`, load in the
// daemon and still be refused by sync with nothing said about why.
const (
	// MaxIDLength bounds every identifier a person writes: handset, group,
	// schedule, person, action and trunk ids. The sync wire carries ids of up
	// to 64 characters and derives a handset's own phonebook id by prefixing
	// four, so 60 is what always fits.
	MaxIDLength = 60
	// MaxLabelLength bounds the labels a household reads on a screen: a
	// handset's, an extension's and a line's.
	MaxLabelLength = 128
	// MaxNameLength bounds a [[people]] name.
	MaxNameLength = 256
	// MaxNumbersPerPerson bounds one [[people]] entry's numbers.
	MaxNumbersPerPerson = 32
	// MaxHandsets bounds the inventory, and every list of handsets.
	MaxHandsets = 100
	// MaxSchedules bounds one policy file's [[schedules]].
	MaxSchedules = 100
	// MaxStringLength and MaxListLength bound every other value and list.
	MaxStringLength = 2048
	MaxListLength   = 1000
	// MaxStepSeconds and MaxStepRings bound one ladder stage.
	MaxStepSeconds = 86400
	MaxStepRings   = 1000
)

// textLength counts the way the sync wire's JSON Schema counts: UTF-16 code
// units, so a label of emoji is measured exactly as the far end measures it.
func textLength(s string) int {
	n := 0
	for _, r := range s { // invalid UTF-8 ranges as U+FFFD, one unit
		n += utf16.RuneLen(r)
	}
	return n
}

// checkLengths walks a decoded file and reports any string or list longer
// than the generic bounds. Messages name the key and never the value: the
// value may be a PIN, a number or a URL holding a token.
func checkLengths(v any, file string, fail func(string, ...any)) {
	var walk func(rv reflect.Value, path string)
	walk = func(rv reflect.Value, path string) {
		switch rv.Kind() {
		case reflect.Interface, reflect.Pointer:
			if !rv.IsNil() {
				walk(rv.Elem(), path)
			}
		case reflect.String:
			if textLength(rv.String()) > MaxStringLength {
				fail("%s %s is longer than %d characters", file, path, MaxStringLength)
			}
		case reflect.Slice:
			if rv.Len() > MaxListLength {
				fail("%s %s lists more than %d entries", file, path, MaxListLength)
				return
			}
			for i := 0; i < rv.Len(); i++ {
				walk(rv.Index(i), path)
			}
		case reflect.Struct:
			t := rv.Type()
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
				if !f.IsExported() || name == "" || name == "-" {
					continue
				}
				child := name
				if path != "" {
					child = path + "." + name
				}
				walk(rv.Field(i), child)
			}
		}
	}
	walk(reflect.ValueOf(v), "")
}

// checkBounds is the part of policy validation that is about size rather than
// meaning. It runs on every load path, the daemon's included.
func checkBounds(f File, fail func(string, ...any)) {
	checkLengths(f, "policy", fail)

	id := func(kind, value string) {
		if textLength(value) > MaxIDLength {
			fail("%s id %q is longer than %d characters", kind, truncateForMessage(value), MaxIDLength)
		}
	}
	label := func(where, value string) {
		if textLength(value) > MaxLabelLength {
			fail("%s label is longer than %d characters", where, MaxLabelLength)
		}
	}
	handsetList := func(where string, ids []string) {
		if len(ids) > MaxHandsets {
			fail("%s lists more than %d handsets", where, MaxHandsets)
		}
	}

	if len(f.Handsets) > MaxHandsets {
		fail("the inventory has %d handsets; the most one box serves is %d", len(f.Handsets), MaxHandsets)
	}
	for _, h := range f.Handsets {
		id("handset", h.ID)
		label(fmt.Sprintf("handset %q", truncateForMessage(h.ID)), h.Label)
		if len(h.Phonebook) > MaxHandsets {
			fail("handset %q lists more than %d phonebooks", truncateForMessage(h.ID), MaxHandsets)
		}
	}
	for _, g := range f.Groups {
		id("group", g.ID)
		handsetList(fmt.Sprintf("group %q", truncateForMessage(g.ID)), g.Handsets)
	}
	if len(f.Schedules) > MaxSchedules {
		fail("[[schedules]] has %d entries; the most a policy holds is %d", len(f.Schedules), MaxSchedules)
	}
	for _, sc := range f.Schedules {
		id("schedule", sc.ID)
	}
	handsetList("[house]", f.House.Handsets)
	label("[line]", f.Line.Label)
	for _, p := range f.People {
		id("person", p.ID)
		if textLength(p.Name) > MaxNameLength {
			fail("a [[people]] name is longer than %d characters", MaxNameLength)
		}
		if len(p.Numbers) > MaxNumbersPerPerson {
			fail("person %q has more than %d numbers", truncateForMessage(p.Name), MaxNumbersPerPerson)
		}
	}
	for _, a := range f.Actions {
		id("action", a.ID)
	}
	for _, e := range f.Extensions {
		where := fmt.Sprintf("extension %q", truncateForMessage(e.Label))
		label(where, e.Label)
		handsetList(where, e.Handsets)
		handsetList(where+" afterhours_ring", e.AfterhoursRing)
		for i, st := range e.Steps {
			stepWhere := fmt.Sprintf("%s step %d", where, i+1)
			handsetList(stepWhere, st.Handsets)
			// Zero is "not set", as it always has been; anything else has to
			// be a duration a phone can actually ring for.
			if st.Rings < 0 || st.Rings > MaxStepRings {
				fail("%s rings must be between 1 and %d", stepWhere, MaxStepRings)
			}
			if st.Seconds < 0 || st.Seconds > MaxStepSeconds {
				fail("%s seconds must be between 1 and %d", stepWhere, MaxStepSeconds)
			}
		}
	}
}

// checkTrunkBounds is checkBounds for trunks.toml.
func checkTrunkBounds(f TrunkFile, fail func(string, ...any)) {
	checkLengths(f, "trunks", fail)
	for _, tr := range f.Trunks {
		if textLength(tr.ID) > MaxIDLength {
			fail("trunk id %q is longer than %d characters", truncateForMessage(tr.ID), MaxIDLength)
		}
		if math.IsNaN(tr.BalanceMin) || math.IsInf(tr.BalanceMin, 0) || tr.BalanceMin > 1<<53-1 {
			fail("trunk %q balance_min must be a finite amount", truncateForMessage(tr.ID))
		}
	}
}

// truncateForMessage keeps an over-long identifier from turning one problem
// into a wall of text.
func truncateForMessage(s string) string {
	if len(s) <= 64 {
		return s
	}
	cut := 61
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

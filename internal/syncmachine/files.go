// Package syncmachine observes configuration and executes typed, versioned
// changes outside the call path. Files are authoritative; SQLite is an echo and
// a receipt ledger, never a source for rebuilding an operator's configuration.
package syncmachine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	contactbook "callmemaybe/internal/contacts"
	"callmemaybe/internal/ownbook"
	"callmemaybe/internal/policy"
	"callmemaybe/internal/provision"
	"callmemaybe/internal/syncwire"

	"github.com/BurntSushi/toml"
)

var (
	ErrFiles   = errors.New("sync: configuration unavailable or invalid; keeping the last good snapshot")
	ErrChanged = errors.New("sync: files changed while preparing the operation")
	ErrState   = errors.New("sync: cannot safely persist local state")
)

type Paths struct {
	Policy      string
	Handsets    string
	Trunks      string
	Contacts    string
	CountryCode string
	Phonebooks  string
}

type observed struct {
	configuration syncwire.Configuration
	files         map[string][]byte
	policies      map[string]string
}

func readFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrFiles
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, ErrFiles
	}
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return nil, ErrFiles
	}
	return data, nil
}

func tomlObject(data []byte) (map[string]any, error) {
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil, ErrFiles
	}
	// TOML arrays of tables have a different Go shape; JSON normalises them.
	b, err := json.Marshal(raw)
	if err != nil || json.Unmarshal(b, &raw) != nil {
		return nil, ErrFiles
	}
	return raw, nil
}

func rows(v map[string]any, key string) []map[string]any {
	arr, _ := v[key].([]any)
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func str(v map[string]any, key string) string { s, _ := v[key].(string); return s }
func list(v map[string]any, key string) []any {
	a, _ := v[key].([]any)
	if a == nil {
		return []any{}
	}
	return a
}
func enabled(v map[string]any) bool { b, ok := v["enabled"].(bool); return !ok || b }
func stableID(prefix string, parts ...string) string {
	data, _ := json.Marshal(parts)
	sum := sha256.Sum256(data)
	return prefix + hex.EncodeToString(sum[:16])
}

func observe(paths Paths) (*observed, error) {
	hdata, err := readFile(paths.Handsets)
	if err != nil {
		return nil, err
	}
	h, err := tomlObject(hdata)
	if err != nil {
		return nil, err
	}
	hpublic, err := syncwire.Project("HandsetValues", h)
	if err != nil {
		return nil, ErrFiles
	}
	out := &observed{files: map[string][]byte{paths.Handsets: hdata}, policies: map[string]string{}}
	options := policy.Options{ModelKnown: provision.Known, StrictUnknownKeys: true}
	context := map[string]any{"validator": "doorman-37612e50d5d7", "handsets": hpublic, "policies": []any{}}
	if paths.Trunks != "" {
		if _, err := os.Stat(paths.Trunks); err == nil {
			data, err := readFile(paths.Trunks)
			if err != nil {
				return nil, err
			}
			options.Trunks, err = policy.TrunksFromTOML(data)
			if err != nil {
				return nil, ErrFiles
			}
			raw, err := tomlObject(data)
			if err != nil {
				return nil, err
			}
			context["trunks"], err = syncwire.Project("TrunkValues", raw)
			if err != nil {
				return nil, ErrFiles
			}
			out.files[paths.Trunks] = data
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, ErrFiles
		}
	}
	handsets, contacts, books, policies := []any{}, []any{}, []any{}, []any{}
	books = append(books, map[string]any{"id": "house", "label": "House", "contacts": []any{}, "includes": []any{}})
	peopleIDs := []any{}
	lines, _ := policy.DiscoverLines(paths.Policy)
	for _, line := range lines {
		data, err := readFile(line.Path)
		if err != nil || len(policy.LintSplitWith(data, hdata, options)) != 0 {
			return nil, ErrFiles
		}
		raw, err := tomlObject(data)
		if err != nil {
			return nil, err
		}
		out.files[line.Path], out.policies[line.Name] = data, line.Path
		people, schedules, extensions := []any{}, []any{}, []any{}
		for i, person := range rows(raw, "people") {
			id := str(person, "id")
			if id == "" {
				id = fmt.Sprint(i)
			}
			id = stableID("person:", line.Name, id)
			numbers := []any{}
			for _, n := range list(person, "numbers") {
				s, _ := n.(string)
				normalized := policy.NormaliseCallerID(s, "1").Value
				numbers = append(numbers, map[string]any{"label": "phone", "number": normalized})
			}
			c := map[string]any{"id": id, "name": str(person, "name"), "numbers": numbers}
			people, contacts = append(people, c), append(contacts, c)
			if line.Name == policy.DefaultLine {
				peopleIDs = append(peopleIDs, id)
			}
		}
		for _, s := range rows(raw, "schedules") {
			schedules = append(schedules, map[string]any{"id": s["id"], "start": s["start"], "end": s["end"], "days": list(s, "days"), "enabled": enabled(s)})
		}
		for i, e := range rows(raw, "extensions") {
			e["pinSet"] = str(e, "pin") != ""
			delete(e, "pin")
			item := map[string]any{"id": fmt.Sprintf("extension:%d", i), "label": str(e, "label"), "handsets": list(e, "handsets"), "pinSet": e["pinSet"], "enabled": enabled(e)}
			if v := str(e, "afterhours"); v != "" {
				item["afterhours"] = v
			}
			extensions = append(extensions, item)
		}
		for _, action := range rows(raw, "actions") {
			action["webhookSet"] = str(action, "webhook") != ""
			delete(action, "webhook")
		}
		public, err := syncwire.Project("PolicyValues", raw)
		if err != nil {
			return nil, ErrFiles
		}
		context["policies"] = append(context["policies"].([]any), map[string]any{"id": line.Name, "values": public})
		house, _ := raw["house"].(map[string]any)
		label := line.Name
		if v, ok := raw["line"].(map[string]any); ok && str(v, "label") != "" {
			label = str(v, "label")
		}
		policies = append(policies, map[string]any{"id": line.Name, "label": label, "houseHandsets": list(house, "handsets"), "people": people, "schedules": schedules, "extensions": extensions})
	}
	books = append(books, map[string]any{"id": "people", "label": "People", "contacts": peopleIDs, "includes": []any{}})
	if paths.Contacts != "" {
		sources, err := policy.LoadContacts(paths.Contacts)
		if err != nil {
			return nil, ErrFiles
		}
		if sources.Present() {
			data, err := readFile(paths.Contacts)
			if err != nil {
				return nil, err
			}
			out.files[paths.Contacts] = data
			// Source/cache bytes participate in the same revision check as the
			// inventory. A concurrent contact refresh must not let a web edit
			// validate against a different directory from the one it replaces.
			for _, source := range sources.All() {
				path := source.Path
				if path == "" {
					path = filepath.Join(sources.CacheDir(), source.ID+".vcf")
				} else if !filepath.IsAbs(path) {
					path = filepath.Join(filepath.Dir(paths.Contacts), path)
				}
				data, err := readFile(path)
				if err != nil {
					return nil, err
				}
				out.files[path] = data
			}
			country := paths.CountryCode
			if country == "" {
				country = "1"
			}
			set := contactbook.Load(sources, country)
			bySource := map[string][]any{}
			for _, report := range set.Sources() {
				if report.Unread != "" || report.Malformed > 0 || report.Dropped > 0 {
					return nil, ErrFiles
				}
				if report.Kind == policy.ContactAdmit {
					bySource[report.ID] = []any{}
				}
			}
			for _, number := range set.Numbers() {
				entry, ok := set.Lookup(number)
				if !ok || entry.Blocked || entry.Name == "" {
					continue
				}
				id := stableID("source:", entry.Source, number)
				contacts = append(contacts, map[string]any{"id": id, "name": entry.Name, "numbers": []any{map[string]any{"label": "phone", "number": number}}})
				bySource[entry.Source] = append(bySource[entry.Source], id)
			}
			for _, source := range sources.All() {
				if ids, ok := bySource[source.ID]; ok {
					books = append(books, map[string]any{"id": source.ID, "label": source.ID, "contacts": ids, "includes": []any{}})
				}
			}
		}
	}
	if paths.Phonebooks != "" {
		// Handset-filed cards and CLI imports are directory-only additions.
		// They never become an admission list or another handset's selection.
		add := func(dir, id, bookID, label string, existing map[string]any) error {
			path := ownbook.Path(dir, id)
			if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
				return nil
			}
			data, err := readFile(path)
			if err != nil {
				return err
			}
			out.files[path] = data
			book, err := ownbook.Load(dir, id)
			if err != nil {
				return ErrFiles
			}
			haveIDs, haveNumbers := map[string]bool{}, map[string]bool{}
			for _, id := range list(existing, "contacts") {
				if id, ok := id.(string); ok {
					haveIDs[id] = true
				}
			}
			for _, contact := range contacts {
				c := contact.(map[string]any)
				if haveIDs[str(c, "id")] {
					for _, number := range rows(c, "numbers") {
						haveNumbers[str(number, "number")] = true
					}
				}
			}
			ids := []any{}
			for _, entry := range book.Entries {
				if haveNumbers[entry.E164] {
					continue
				}
				haveNumbers[entry.E164] = true
				contactID := stableID("filed:", path, entry.UID)
				ids = append(ids, contactID)
				contacts = append(contacts, map[string]any{"id": contactID, "name": entry.Name, "numbers": []any{map[string]any{"label": "phone", "number": entry.E164}}})
			}
			if existing != nil {
				existing["contacts"] = append(list(existing, "contacts"), ids...)
			} else {
				books = append(books, map[string]any{"id": bookID, "label": label, "contacts": ids, "includes": []any{}})
			}
			return nil
		}
		house := books[0].(map[string]any)
		if err := add(filepath.Join(paths.Phonebooks, "shared"), "house", "house", "House", house); err != nil {
			return nil, err
		}
		for _, row := range rows(h, "handsets") {
			id := str(row, "id")
			book := map[string]any{"id": "own:" + id, "label": str(row, "label") + " additions", "contacts": []any{}, "includes": []any{}}
			for _, dir := range []string{filepath.Join(paths.Phonebooks, "own"), filepath.Join(paths.Phonebooks, "shared", "handsets")} {
				if err := add(dir, id, "own:"+id, str(row, "label"), book); err != nil {
					return nil, err
				}
			}
			if len(list(book, "contacts")) > 0 {
				books = append(books, book)
			}
		}
	}
	for _, row := range rows(h, "handsets") {
		id := str(row, "id")
		selected := list(row, "phonebook")
		if len(selected) == 0 {
			selected = []any{"house", "people"}
		}
		bookID := stableID("selection:", id)
		books = append(books, map[string]any{"id": bookID, "label": str(row, "label"), "contacts": []any{}, "includes": selected})
		handsets = append(handsets, map[string]any{"id": id, "label": str(row, "label"), "phonebook": bookID, "pinSet": false})
	}
	data, err := json.Marshal(map[string]any{"handsets": handsets, "contacts": contacts, "phonebooks": books, "policies": policies, "validation": context})
	if err != nil || len(data) > 1<<20 || syncwire.Decode("Configuration", data, &out.configuration) != nil {
		return nil, ErrFiles
	}
	if !out.unchanged() {
		return nil, ErrChanged
	}
	return out, nil
}

func (o *observed) unchanged() bool {
	for path, before := range o.files {
		after, err := readFile(path)
		if err != nil || !bytes.Equal(before, after) {
			return false
		}
	}
	return true
}

func (o *observed) digest() string {
	keys := make([]string, 0, len(o.files))
	for k := range o.files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		// Length framing keeps different splits of file contents distinct.
		fmt.Fprintf(h, "%d:%s:%d:", len(k), k, len(o.files[k]))
		h.Write(o.files[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

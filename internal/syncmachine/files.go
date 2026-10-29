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
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"callmemaybe/internal/accountbook"
	"callmemaybe/internal/configcheck"
	contactbook "callmemaybe/internal/contacts"
	"callmemaybe/internal/ownbook"
	"callmemaybe/internal/podcastconfig"
	"callmemaybe/internal/policy"
	"callmemaybe/internal/syncwire"
	"callmemaybe/internal/webhookconfig"

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
	Webhook     string
	Podcasts    string
}

type observed struct {
	configuration syncwire.Configuration
	files         map[string][]byte
	policies      map[string]string
	// boxDigest names the configuration without the account-managed books
	// and their contacts. A delivered book changes the snapshot revision,
	// but an edit proposed against the box's own files is still current if
	// nothing else moved.
	boxDigest     string
	webhookDigest string
}

var (
	errMissing    = fmt.Errorf("%w: missing", ErrFiles)
	errUnreadable = fmt.Errorf("%w: unreadable", ErrFiles)
	errTooLarge   = fmt.Errorf("%w: not a regular file of at most 1 MiB", ErrFiles)
)

func readFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errMissing
	}
	if err != nil {
		return nil, errUnreadable
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, errUnreadable
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, errTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return nil, errUnreadable
	}
	if len(data) > 1<<20 {
		return nil, errTooLarge
	}
	return data, nil
}

// Rejection is why the newest configuration on disk was not accepted. The
// last good revision stays in the echo; the rejection is logged once per
// distinct content and reported in the status sync sends, until a valid
// configuration replaces it. Message is redacted and bounded: it leaves the
// box.
type Rejection struct {
	File    string // base name only: a path can carry an account name
	Message string
	key     string // identifies the rejected content, for logging once
}

func (r *Rejection) Error() string {
	return "sync: " + r.File + " rejected, keeping the last good configuration: " + r.Message
}

func (r *Rejection) Unwrap() error { return ErrFiles }

// rejecter accumulates the bytes a pass has read, so a rejection can say which
// content it was about without keeping the content.
type rejecter struct{ seen hash.Hash }

func (j *rejecter) read(path string) ([]byte, error) {
	data, err := readFile(path)
	if j.seen == nil {
		j.seen = sha256.New()
	}
	fmt.Fprintf(j.seen, "%s\x00%d\x00", path, len(data))
	j.seen.Write(data)
	if err != nil {
		fmt.Fprintf(j.seen, "error:%v", err)
	}
	return data, err
}

func (j *rejecter) reject(path string, problems ...string) *Rejection {
	if j.seen == nil {
		j.seen = sha256.New()
	}
	msg := policy.RedactProblem(strings.Join(problems, "; "), policy.MaxProblemLength)
	if msg == "" {
		msg = "invalid"
	}
	file := filepath.Base(path)
	return &Rejection{File: file, Message: msg, key: hex.EncodeToString(j.seen.Sum(nil)) + "\x00" + file + "\x00" + msg}
}

// unreadable is the rejection for a file that could not be read at all.
func (j *rejecter) unreadable(path string, err error) *Rejection {
	switch {
	case errors.Is(err, errMissing):
		return j.reject(path, "the file is missing")
	case errors.Is(err, errTooLarge):
		return j.reject(path, "not a regular file of at most 1 MiB")
	}
	return j.reject(path, "the file cannot be read")
}

// handsetsProblem reports whether a shared-validator problem is about the
// inventory rather than the policy file it was validated with, so the
// rejection names the file a person has to edit.
func handsetsProblem(p string) bool {
	for _, prefix := range []string{"handsets:", "handset ", "handsets ", "group ", "at least one [[handsets]]", "the inventory "} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return strings.Contains(p, " in [[handsets") || strings.Contains(p, " in [[groups")
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

// afterhours is an extension's afterhours as the wire carries it: one id as
// the string it has always been, several as a list. A list of one id means
// exactly that id, and is sent as the string, so the two spellings of one
// schedule are the same snapshot — the one a service that predates lists
// has always accepted.
func afterhours(e map[string]any) (any, bool) {
	switch v := e["afterhours"].(type) {
	case string:
		return v, v != ""
	case []any:
		if len(v) == 1 {
			if id, ok := v[0].(string); ok {
				return id, id != ""
			}
		}
		return v, len(v) > 0
	}
	return nil, false
}
func stableID(prefix string, parts ...string) string {
	data, _ := json.Marshal(parts)
	sum := sha256.Sum256(data)
	return prefix + hex.EncodeToString(sum[:16])
}

// observe reads the configuration and accepts it only when the shared
// validator (configcheck — the rules `doorman check` and the daemon's reload
// apply) accepts every file. Anything refused comes back as a *Rejection
// naming the file and the problem.
func observe(paths Paths) (*observed, error) {
	var j rejecter
	hdata, err := j.read(paths.Handsets)
	if err != nil {
		if errors.Is(err, errMissing) {
			// The legacy single-file layout loads in the daemon, but the sync
			// protocol carries the inventory on its own.
			return nil, j.reject(paths.Handsets, "the file is missing; sync needs the two-file layout, with handsets.toml beside policy.toml")
		}
		return nil, j.unreadable(paths.Handsets, err)
	}
	out := &observed{files: map[string][]byte{paths.Handsets: hdata}, policies: map[string]string{}}
	// No trunks.toml is an absent inventory, never an unchecked one: a line
	// naming a trunk is told there is no trunks.toml, as `doorman check` and
	// the daemon tell it.
	options := configcheck.Options(nil)
	context := map[string]any{"validator": "doorman-37612e50d5d7", "policies": []any{}}
	if paths.Trunks != "" {
		data, err := j.read(paths.Trunks)
		switch {
		case errors.Is(err, errMissing):
		case err != nil:
			return nil, j.unreadable(paths.Trunks, err)
		default:
			if problems := policy.LintTrunks(data); len(problems) != 0 {
				return nil, j.reject(paths.Trunks, problems...)
			}
			trunks, err := policy.TrunksFromTOML(data)
			if err != nil {
				return nil, j.reject(paths.Trunks, err.Error())
			}
			options = configcheck.Options(trunks)
			raw, err := tomlObject(data)
			if err != nil {
				return nil, j.reject(paths.Trunks, "not valid TOML")
			}
			context["trunks"], err = syncwire.Project("TrunkValues", raw)
			if err != nil {
				return nil, j.reject(paths.Trunks, "sync cannot represent "+syncwire.Explain("TrunkValues", raw))
			}
			out.files[paths.Trunks] = data
		}
	}
	lines, _ := policy.DiscoverLines(paths.Policy)
	linesData := make([][]byte, len(lines))
	for i, line := range lines {
		data, err := j.read(line.Path)
		if err != nil {
			return nil, j.unreadable(line.Path, err)
		}
		if problems := policy.LintSplitWith(data, hdata, options); len(problems) != 0 {
			path := line.Path
			if handsetsProblem(problems[0]) {
				path = paths.Handsets
			}
			return nil, j.reject(path, problems...)
		}
		linesData[i] = data
	}
	// Valid by the shared rules from here on. What follows is projection onto
	// the sync wire; a failure is a bound the validator does not know about,
	// and is reported by its path rather than refused in silence.
	h, err := tomlObject(hdata)
	if err != nil {
		return nil, j.reject(paths.Handsets, "not valid TOML")
	}
	for _, row := range rows(h, "handsets") {
		// Any written form of a MAC is valid; the wire carries the canonical.
		if mac, ok := policy.NormaliseMAC(str(row, "mac")); ok {
			row["mac"] = mac
		}
	}
	hpublic, err := syncwire.Project("HandsetValues", h)
	if err != nil {
		return nil, j.reject(paths.Handsets, "sync cannot represent "+syncwire.Explain("HandsetValues", h))
	}
	context["handsets"] = hpublic
	handsets, contacts, books, policies := []any{}, []any{}, []any{}, []any{}
	books = append(books, map[string]any{"id": "house", "label": "House", "contacts": []any{}, "includes": []any{}})
	peopleIDs := []any{}
	for i, line := range lines {
		data := linesData[i]
		raw, err := tomlObject(data)
		if err != nil {
			return nil, j.reject(line.Path, "not valid TOML")
		}
		for _, s := range rows(raw, "schedules") {
			// Day codes are case-insensitive to every reader on the box, and a
			// repeated day means nothing more than the day once.
			if _, ok := s["days"]; ok {
				days, seen := []any{}, map[string]bool{}
				for _, d := range list(s, "days") {
					if d, ok := d.(string); ok {
						d = strings.ToUpper(d)
						if !seen[d] {
							seen[d] = true
							days = append(days, d)
						}
					}
				}
				s["days"] = days
			}
			// "7:00" is 07:00 to the validator; the wire carries the latter.
			for _, field := range []string{"start", "end"} {
				if v := str(s, field); len(v) == 4 && v[1] == ':' {
					s[field] = "0" + v
				}
			}
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
			if v, ok := afterhours(e); ok {
				e["afterhours"], item["afterhours"] = v, v
			}
			extensions = append(extensions, item)
		}
		for _, action := range rows(raw, "actions") {
			action["webhookSet"] = str(action, "webhook") != ""
			delete(action, "webhook")
		}
		public, err := syncwire.Project("PolicyValues", raw)
		if err != nil {
			return nil, j.reject(line.Path, "sync cannot represent "+syncwire.Explain("PolicyValues", raw))
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
			return nil, j.reject(paths.Contacts, err.Error())
		}
		if sources.Present() {
			data, err := j.read(paths.Contacts)
			if err != nil {
				return nil, j.unreadable(paths.Contacts, err)
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
				data, err := j.read(path)
				if err != nil {
					return nil, j.unreadable(path, err)
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
					// Counts only: a card's content is a person's name and number.
					return nil, j.reject(paths.Contacts, fmt.Sprintf("address book %q could not be read whole: %d cards dropped, %d malformed entries", report.ID, report.Dropped, report.Malformed))
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
			data, err := j.read(path)
			if err != nil {
				return j.unreadable(path, err)
			}
			out.files[path] = data
			book, err := ownbook.Load(dir, id)
			if err != nil {
				return j.reject(path, "the phonebook file is not valid")
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
			book := map[string]any{"id": "own:" + id, "label": wireLabel(str(row, "label") + " additions"), "contacts": []any{}, "includes": []any{}}
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
	// Account-managed books: directory only — except the one lobby pass
	// book, which no directory shows — validated as a set with the
	// inventory that uses them. Checked even with no phonebook directory, so
	// a handset naming a book the box does not hold is refused here as it is
	// by the directory.
	accountDir, accountFiles := "", map[string][]byte{}
	if paths.Phonebooks != "" {
		accountDir = filepath.Join(paths.Phonebooks, accountbook.Dir)
		names, err := accountNames(accountDir)
		if err != nil {
			return nil, j.unreadable(accountDir, err)
		}
		for _, name := range names {
			path := filepath.Join(accountDir, name)
			data, err := j.read(path)
			if err != nil {
				return nil, j.unreadable(path, err)
			}
			out.files[path], accountFiles[name] = data, data
		}
	}
	handsetList, _, err := policy.HandsetsFromTOML(hdata)
	if err != nil {
		return nil, j.reject(paths.Handsets, err.Error())
	}
	held, problems := configcheck.AccountBooks(accountFiles, handsetList)
	if len(problems) > 0 {
		path := paths.Handsets
		if problems[0].File != "" {
			path = filepath.Join(accountDir, problems[0].File)
		}
		messages := make([]string, 0, len(problems))
		for _, p := range problems {
			messages = append(messages, p.Message)
		}
		return nil, j.reject(path, messages...)
	}
	accountContacts := map[string]bool{}
	heldIDs := make([]string, 0, len(held))
	for id := range held {
		heldIDs = append(heldIDs, id)
	}
	sort.Strings(heldIDs)
	for _, id := range heldIDs {
		b := held[id]
		ids := []any{}
		for _, c := range b.Contacts {
			ids = append(ids, c.ID)
			if accountContacts[c.ID] {
				// One contact in several books is one contact. Copies of
				// different revisions may differ for a moment; the first
				// book's copy is the one echoed.
				continue
			}
			accountContacts[c.ID] = true
			numbers := []any{}
			for _, n := range c.Numbers {
				numbers = append(numbers, map[string]any{"label": n.Label, "number": n.Number})
			}
			contacts = append(contacts, map[string]any{"id": c.ID, "name": c.Name, "numbers": numbers})
		}
		includes := []any{}
		for _, inc := range b.Includes {
			includes = append(includes, inc)
		}
		echo := map[string]any{"id": id, "label": b.Label, "contacts": ids, "includes": includes,
			"origin": "account", "revision": strconv.FormatInt(b.Revision, 10),
			"hash": accountbook.Hash(accountFiles[accountbook.FileName(id)])}
		if b.Role != "" {
			echo["role"] = b.Role
		}
		books = append(books, echo)
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
	boxBooks := []any{}
	for _, b := range books {
		book := b.(map[string]any)
		if _, ok := book["origin"]; !ok {
			book["origin"] = "box"
		}
		if book["origin"] == "box" {
			boxBooks = append(boxBooks, book)
		}
	}
	boxContacts := []any{}
	for _, c := range contacts {
		if !accountContacts[str(c.(map[string]any), "id")] {
			boxContacts = append(boxContacts, c)
		}
	}
	var webhook *syncwire.WebhookState
	if paths.Webhook != "" {
		data, err := j.read(paths.Webhook)
		c := webhookconfig.Config{}
		if err != nil && !errors.Is(err, errMissing) {
			return nil, j.unreadable(paths.Webhook, err)
		}
		if err == nil {
			c, err = webhookconfig.Parse(data)
			if err != nil {
				return nil, j.reject(paths.Webhook, "invalid webhook domain or path prefix")
			}
		}
		out.files[paths.Webhook] = data
		out.webhookDigest = webhookconfig.Hash(data)
		webhook = &syncwire.WebhookState{Domain: c.Domain, Configured: c.Domain != ""}
	}
	var subscriptions []podcastconfig.Subscription
	var podcastFeeds = []podcastconfig.Source{}
	if paths.Podcasts != "" {
		data, err := j.read(paths.Podcasts)
		if err != nil && !errors.Is(err, errMissing) {
			return nil, j.unreadable(paths.Podcasts, err)
		}
		c, err := podcastconfig.Parse(data)
		if err != nil {
			return nil, j.reject(paths.Podcasts, "invalid podcast subscriptions")
		}
		known := map[string]bool{}
		for _, raw := range handsets {
			known[str(raw.(map[string]any), "id")] = true
		}
		for _, sub := range c.Subscriptions {
			if !known[sub.Handset] {
				return nil, j.reject(paths.Podcasts, "unknown podcast handset")
			}
		}
		subscriptions = c.Subscriptions
		if c.Feeds != nil {
			podcastFeeds = c.Feeds
		}
		out.files[paths.Podcasts] = data
	}
	digest, err := json.Marshal(map[string]any{"podcasts": subscriptions, "podcastFeeds": podcastFeeds, "handsets": handsets, "contacts": boxContacts, "phonebooks": boxBooks, "policies": policies, "validation": context})
	if err != nil {
		return nil, j.reject(paths.Policy, "sync cannot represent the configuration")
	}
	out.boxDigest = fileHash(append(digest, []byte(out.webhookDigest)...))
	configuration := map[string]any{"handsets": handsets, "contacts": contacts, "phonebooks": books, "policies": policies, "validation": context}
	if webhook != nil {
		configuration["webhook"] = webhook
	}
	if paths.Podcasts != "" {
		configuration["podcasts"] = subscriptions
		configuration["podcastFeeds"] = podcastFeeds
	}
	data, err := json.Marshal(configuration)
	if err != nil {
		return nil, j.reject(paths.Policy, "sync cannot represent the configuration")
	}
	if len(data) > 1<<20 {
		return nil, j.reject(paths.Policy, "the configuration is larger than the 1 MiB sync carries")
	}
	if syncwire.Decode("Configuration", data, &out.configuration) != nil {
		where := syncwire.Explain("Configuration", configuration)
		return nil, j.reject(wireFile(paths, lines, where), "sync cannot represent "+where)
	}
	if !out.unchanged() {
		return nil, ErrChanged
	}
	return out, nil
}

// accountNames lists the managed book files in dir, sorted. A missing
// directory holds none; dotfiles are a writer's temporary files.
func accountNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errUnreadable
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		names = append(names, e.Name())
	}
	return names, nil
}

// wireLabel shortens a label the observer composes to what the wire carries.
func wireLabel(s string) string {
	const max = 128
	if len(utf16.Encode([]rune(s))) <= max {
		return s
	}
	n := 0
	for i, r := range s {
		if n+utf16.RuneLen(r) > max-1 {
			return s[:i] + "…"
		}
		n += utf16.RuneLen(r)
	}
	return s
}

// wireFile names the file a Configuration path came from, for a rejection.
func wireFile(paths Paths, lines []policy.LineFile, where string) string {
	var i int
	if _, err := fmt.Sscanf(where, "policies[%d]", &i); err == nil && i >= 0 && i < len(lines) {
		return lines[i].Path
	}
	switch {
	case strings.HasPrefix(where, "handsets"):
		return paths.Handsets
	case strings.HasPrefix(where, "contacts"), strings.HasPrefix(where, "phonebooks"):
		if paths.Contacts != "" {
			return paths.Contacts
		}
	}
	return paths.Policy
}

func (o *observed) unchanged() bool {
	for path, before := range o.files {
		after, err := readFile(path)
		if before == nil && errors.Is(err, errMissing) {
			continue
		}
		if err != nil || !bytes.Equal(before, after) {
			return false
		}
	}
	return true
}

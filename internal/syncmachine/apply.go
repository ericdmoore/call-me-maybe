package syncmachine

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"callmemaybe/internal/accountbook"
	"callmemaybe/internal/configcheck"
	"callmemaybe/internal/policy"
	"callmemaybe/internal/syncwire"
)

func problem(p syncwire.ReceiptProblem) *syncwire.ReceiptProblem { return &p }

func fileHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// write is one file a command changes. Before and After are the content
// hashes on either side, "" for a file that does not exist: a delivered
// book is created, and a removed one is gone.
type write struct {
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// state is a file's hash in a view, "" when the view has no such file.
func state(files map[string][]byte, path string) string {
	data, ok := files[path]
	if !ok {
		return ""
	}
	return fileHash(data)
}

// refusal is a final answer found while planning a command.
type refusal struct {
	status  syncwire.ReceiptStatus
	problem syncwire.ReceiptProblem
}

func refuse(p syncwire.ReceiptProblem) *refusal {
	return &refusal{syncwire.ReceiptStatusRejected, p}
}

var conflicted = &refusal{syncwire.ReceiptStatusConflict, syncwire.BaseChanged}

// verbs is a queue item's ordered list: its verbs, or its one verb. An item
// with both or neither has no meaning.
func verbs(item syncwire.QueueItem) ([]syncwire.Verb, bool) {
	switch {
	case item.Verb != nil && item.Verbs == nil:
		return []syncwire.Verb{*item.Verb}, true
	case item.Verb == nil && item.Verbs != nil:
		return *item.Verbs, true
	}
	return nil, false
}

// Apply records intent before touching disk. A replay after a crash recognises
// the target files' written bytes and finishes its receipt without repeating
// a write. Neither a stale web edit nor a failed parse overwrites local work.
// The intent is the targets' own hashes, not the whole observed set: a
// contacts refresh or an unrelated inventory edit must not turn a landed
// write into a conflict.
//
// An item's verbs apply in order with one receipt. Every verb is planned and
// the result validated as a whole before anything is written, so a refused
// verb writes nothing. The writes are not one atomic step: delivered books
// are written first, and a book is inert until a handset names it, so a
// failure part way leaves only unreferenced books.
func (m *Machine) Apply(item syncwire.QueueItem) (syncwire.Receipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	body, err := json.Marshal(item)
	if err != nil || syncwire.Decode("QueueItem", body, &item) != nil {
		return syncwire.Receipt{}, syncwire.ErrInvalid
	}
	result := syncwire.Receipt{Id: item.Id, Generation: item.Base.Generation, Status: syncwire.ReceiptStatusRejected}
	save := func() (syncwire.Receipt, error) {
		data, err := json.Marshal(result)
		if err == nil {
			_, err = m.db.Exec("UPDATE commands SET receipt=? WHERE id=?", string(data), item.Id)
		}
		if err != nil {
			return result, ErrState
		}
		return result, nil
	}
	answer := func(r *refusal) (syncwire.Receipt, error) {
		result.Status, result.Problem = r.status, problem(r.problem)
		return save()
	}
	var oldBody string
	var oldReceipt, targetPath, beforeHash, afterHash, writesJSON sql.NullString
	err = m.db.QueryRow("SELECT body,receipt,target_path,before_hash,after_hash,writes FROM commands WHERE id=?", item.Id).Scan(&oldBody, &oldReceipt, &targetPath, &beforeHash, &afterHash, &writesJSON)
	saved := false
	if err == nil {
		if oldBody != string(body) {
			return result, syncwire.ErrInvalid
		}
		if oldReceipt.Valid {
			if syncwire.Decode("Receipt", []byte(oldReceipt.String), &result) != nil {
				return result, ErrState
			}
			if result.Status != syncwire.ReceiptStatusSaved {
				return result, nil
			}
			saved = true
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		if _, err = m.db.Exec("INSERT INTO commands (id,body) VALUES (?,?)", item.Id, string(body)); err != nil {
			return result, ErrState
		}
	} else {
		return result, ErrState
	}
	intent, err := intentOf(targetPath, beforeHash, afterHash, writesJSON)
	if err != nil {
		return result, ErrState
	}
	o, err := observe(m.paths)
	if err != nil {
		if saved {
			return result, nil
		}
		// A file rewritten mid-read (a contacts refresh) or unreadable for now is
		// no answer to the command. Store nothing so the next pass retries it;
		// the command's own expiry bounds the wait.
		return result, err
	}
	current, err := m.record(o)
	if err != nil {
		return result, err
	}
	landed := len(intent) > 0
	for _, w := range intent {
		landed = landed && state(o.files, w.Path) == w.After
	}
	if saved {
		// A saved receipt is already the server's answer. Later edits can only
		// stop it being upgraded; they never turn it into a conflict.
		if landed && m.Applied != nil && m.Applied(m.paths, o.files) {
			result.Status = syncwire.ReceiptStatusApplied
			return save()
		}
		return result, nil
	}
	if current.Generation != item.Base.Generation {
		return answer(&refusal{syncwire.ReceiptStatusStale, syncwire.BaseChanged})
	}
	if landed {
		// The process stopped between the last rename and the receipt.
		result.Status = syncwire.ReceiptStatusSaved
		result.Revision = &current.Revision
		result.Problem = nil
		if m.Applied != nil && m.Applied(m.paths, o.files) {
			result.Status = syncwire.ReceiptStatusApplied
		}
		return save()
	}
	list, ok := verbs(item)
	if !ok {
		return answer(refuse(syncwire.Invalid))
	}
	view := make(map[string][]byte, len(o.files))
	for path, data := range o.files {
		view[path] = data
	}
	if len(intent) > 0 {
		// The process stopped part way through the writes. Each target must
		// still hold what was there or what was written, and planning again
		// over the partly written files must arrive at the same result.
		// Every verb is idempotent over its own result, so it does.
		for _, w := range intent {
			if s := state(o.files, w.Path); s != w.Before && s != w.After {
				return m.abandon(item.Id, answer, conflicted)
			}
		}
		if r := m.plan(list, current, o.policies, view, nil); r != nil {
			return m.abandon(item.Id, answer, r)
		}
		for _, w := range intent {
			if state(view, w.Path) != w.After {
				return m.abandon(item.Id, answer, conflicted)
			}
		}
	} else {
		// Delivering or removing an account book is checked against that
		// book's own bytes; anything touching the box's own files needs the
		// box's own files to be as the proposal saw them.
		if needsBase(list) && !m.sameBase(item.Base.Revision, current) {
			return answer(conflicted)
		}
		if m.now().UnixMilli() >= item.NotAfter {
			return answer(&refusal{syncwire.ReceiptStatusStale, syncwire.Expired})
		}
		var order []string
		if r := m.plan(list, current, o.policies, view, &order); r != nil {
			return answer(r)
		}
		for _, path := range order {
			if before, after := state(o.files, path), state(view, path); before != after {
				intent = append(intent, write{Path: path, Before: before, After: after})
			}
		}
		if len(intent) > 0 {
			// The same rules observe applies, so a change is saved only when
			// the result is a configuration observe — and check, and the
			// daemon — accept.
			if r := m.validate(o, view); r != nil {
				return answer(r)
			}
			if !o.unchanged() {
				return answer(conflicted)
			}
			if err := m.prepare(item.Id, intent); err != nil {
				return result, err
			}
		}
	}
	for _, w := range intent {
		if state(o.files, w.Path) == w.After {
			continue
		}
		var done bool
		switch {
		case w.Before == "":
			done, err = atomicCreate(w.Path, view[w.Path])
		case w.After == "":
			done, err = atomicRemove(w.Path, o.files[w.Path])
		default:
			done, err = atomicReplace(w.Path, o.files[w.Path], view[w.Path])
		}
		if err != nil && done {
			// Leave prepared intent intact. The next pass recognises the
			// target's bytes, so a directory fsync failure after the rename is
			// not repeated.
			return result, err
		}
		if err != nil {
			// Nothing replaced this target (a symlink, or a directory that
			// refuses a temporary file), so the answer is final. Without a
			// receipt the same item would come back every pass and hold every
			// later command. Earlier writes of this item stay: they are
			// delivered books no handset names yet.
			r := &refusal{syncwire.ReceiptStatusFailed, syncwire.ExecutionFailed}
			if errors.Is(err, ErrChanged) {
				r = conflicted
			}
			return m.abandon(item.Id, answer, r)
		}
	}
	next, err := observe(m.paths)
	if err != nil {
		return result, err
	}
	current, err = m.record(next)
	if err != nil {
		return result, err
	}
	result.Status, result.Revision, result.Problem = syncwire.ReceiptStatusSaved, &current.Revision, nil
	for _, w := range intent {
		if state(next.files, w.Path) != w.After {
			result.Status, result.Problem = syncwire.ReceiptStatusConflict, problem(syncwire.BaseChanged)
		}
	}
	if result.Status == syncwire.ReceiptStatusSaved && m.Applied != nil && m.Applied(m.paths, next.files) {
		result.Status = syncwire.ReceiptStatusApplied
	}
	return save()
}

// abandon clears a command's prepared intent and stores its final answer.
func (m *Machine) abandon(id string, answer func(*refusal) (syncwire.Receipt, error), r *refusal) (syncwire.Receipt, error) {
	if _, err := m.db.Exec("UPDATE commands SET target_path=NULL,before_hash=NULL,after_hash=NULL,writes=NULL WHERE id=?", id); err != nil {
		return syncwire.Receipt{}, ErrState
	}
	return answer(r)
}

// prepare records intent before the first write. A single-file command also
// fills the columns earlier releases read, so a downgraded box still
// recognises the write.
func (m *Machine) prepare(id string, intent []write) error {
	data, err := json.Marshal(intent)
	if err != nil {
		return ErrState
	}
	var target, before, after any
	if len(intent) == 1 && intent[0].Before != "" && intent[0].After != "" {
		target, before, after = intent[0].Path, intent[0].Before, intent[0].After
	}
	if _, err = m.db.Exec("UPDATE commands SET target_path=?,before_hash=?,after_hash=?,writes=? WHERE id=?", target, before, after, string(data), id); err != nil {
		return ErrState
	}
	return nil
}

// intentOf reads a command's prepared writes, in either ledger form.
func intentOf(targetPath, beforeHash, afterHash, writesJSON sql.NullString) ([]write, error) {
	if writesJSON.Valid {
		var intent []write
		if json.Unmarshal([]byte(writesJSON.String), &intent) != nil {
			return nil, ErrState
		}
		return intent, nil
	}
	if targetPath.Valid && beforeHash.Valid && afterHash.Valid {
		return []write{{Path: targetPath.String, Before: beforeHash.String, After: afterHash.String}}, nil
	}
	return nil, nil
}

// needsBase reports whether any verb changes the box's own files, which a
// proposal may only do against the configuration its author saw.
func needsBase(list []syncwire.Verb) bool {
	for _, v := range list {
		raw, _ := v.MarshalJSON()
		if name := verbName(raw); name != "phonebook.put" && name != "phonebook.remove" {
			return true
		}
	}
	return false
}

// plan applies verbs, in order, to view: each sees what the ones before it
// did. order, when given, collects the paths touched, first touch first,
// which is the order they are written in.
func (m *Machine) plan(list []syncwire.Verb, current syncwire.Snapshot, policies map[string]string, view map[string][]byte, order *[]string) *refusal {
	touch := func(path string) {
		if order == nil {
			return
		}
		for _, p := range *order {
			if p == path {
				return
			}
		}
		*order = append(*order, path)
	}
	accountDir := ""
	if m.paths.Phonebooks != "" {
		accountDir = filepath.Join(m.paths.Phonebooks, accountbook.Dir)
	}
	for _, v := range list {
		raw, _ := v.MarshalJSON()
		switch verbName(raw) {
		case "policy.setSchedule":
			v, _ := v.AsVerb0()
			path := policies[v.Policy]
			if path == "" {
				return refuse(syncwire.Invalid)
			}
			after, err := patchTable(view[path], "schedules", v.Schedule.Id, map[string]any{"start": v.Schedule.Start, "end": v.Schedule.End, "days": v.Schedule.Days, "enabled": v.Schedule.Enabled})
			if err != nil {
				return refuse(syncwire.Invalid)
			}
			view[path] = after
			touch(path)
		case "phonebook.assign":
			v, _ := v.AsVerb1()
			// Only a selection: book stands for a list: assigning it copies
			// that handset's list. Any other book is assigned as itself, so an
			// account book with contacts and includes keeps both.
			var selected []string
			for _, book := range current.Configuration.Phonebooks {
				if book.Id != v.Phonebook {
					continue
				}
				if strings.HasPrefix(book.Id, "selection:") {
					selected = append([]string{}, book.Includes...)
				} else {
					selected = []string{book.Id}
				}
			}
			if accountbook.ValidBookID(v.Phonebook) {
				selected = nil
				if _, ok := view[filepath.Join(accountDir, accountbook.FileName(v.Phonebook))]; ok && accountDir != "" {
					selected = []string{v.Phonebook}
				}
			}
			if r := m.selectBooks(view, v.Handset, selected, touch); r != nil {
				return r
			}
		case "phonebook.select":
			v, _ := v.AsVerb8()
			known := map[string]bool{}
			for _, book := range current.Configuration.Phonebooks {
				if (book.Origin == nil || *book.Origin == syncwire.Box) && !strings.HasPrefix(book.Id, "own:") && !strings.HasPrefix(book.Id, "selection:") {
					known[book.Id] = true
				}
			}
			for _, id := range v.Books {
				if accountbook.ValidBookID(id) {
					// Whether the box holds it is the validator's question,
					// asked of the result: an earlier verb may deliver it.
					continue
				}
				if !known[id] {
					return refuse(syncwire.Invalid)
				}
			}
			if r := m.selectBooks(view, v.Handset, v.Books, touch); r != nil {
				return r
			}
		case "phonebook.put":
			v, _ := v.AsVerb6()
			if accountDir == "" {
				return refuse(syncwire.Unsupported)
			}
			b, ok := fromWire(v.Book)
			if !ok {
				return refuse(syncwire.Invalid)
			}
			data := accountbook.Encode(b)
			path := filepath.Join(accountDir, accountbook.FileName(b.ID))
			existing, held := view[path]
			if held && !bytes.Equal(existing, data) && (v.BeforeHash == nil || *v.BeforeHash != fileHash(existing)) {
				// Changed on the box, or a delivery that did not know what the
				// box holds: the copy here stays until a delivery names it.
				return conflicted
			}
			if !held && v.BeforeHash != nil {
				return conflicted
			}
			view[path] = data
			touch(path)
		case "phonebook.remove":
			v, _ := v.AsVerb7()
			if accountDir == "" {
				return refuse(syncwire.Unsupported)
			}
			path := filepath.Join(accountDir, accountbook.FileName(v.Book))
			existing, held := view[path]
			if !held {
				continue
			}
			b, err := accountbook.Parse(existing)
			if err != nil || strconv.FormatInt(b.Revision, 10) != v.Revision {
				return conflicted
			}
			delete(view, path)
			touch(path)
		default:
			return refuse(syncwire.Unsupported)
		}
	}
	return nil
}

// selectBooks sets one handset's phonebook list in the view.
func (m *Machine) selectBooks(view map[string][]byte, handset string, books []string, touch func(string)) *refusal {
	if len(books) == 0 {
		return refuse(syncwire.Invalid)
	}
	seen := map[string]bool{}
	for _, b := range books {
		if seen[b] {
			return refuse(syncwire.Invalid)
		}
		seen[b] = true
	}
	path := m.paths.Handsets
	after, err := patchTable(view[path], "handsets", handset, map[string]any{"phonebook": books})
	if err != nil {
		return refuse(syncwire.Invalid)
	}
	view[path] = after
	touch(path)
	return nil
}

// fromWire is a delivered book as the file holds it.
func fromWire(w syncwire.AccountBook) (accountbook.Book, bool) {
	revision, err := strconv.ParseInt(w.Revision, 10, 64)
	if err != nil {
		return accountbook.Book{}, false
	}
	b := accountbook.Book{ID: w.Id, Revision: revision, Label: w.Label, Includes: append([]string{}, w.Includes...)}
	for _, c := range w.Contacts {
		contact := accountbook.Contact{ID: c.Id, Name: c.Name}
		for _, n := range c.Numbers {
			contact.Numbers = append(contact.Numbers, accountbook.Number{Label: n.Label, Number: n.Number})
		}
		b.Contacts = append(b.Contacts, contact)
	}
	return b, len(b.Problems()) == 0
}

// validate checks a planned view by the rules observe applies.
func (m *Machine) validate(o *observed, view map[string][]byte) *refusal {
	options := configcheck.Options(nil)
	if data, ok := view[m.paths.Trunks]; ok {
		trunks, err := policy.TrunksFromTOML(data)
		if err != nil {
			return refuse(syncwire.Invalid)
		}
		options = configcheck.Options(trunks)
	}
	for _, p := range o.policies {
		if len(policy.LintSplitWith(view[p], view[m.paths.Handsets], options)) != 0 {
			return refuse(syncwire.Invalid)
		}
	}
	handsets, _, err := policy.HandsetsFromTOML(view[m.paths.Handsets])
	if err != nil {
		return refuse(syncwire.Invalid)
	}
	books := map[string][]byte{}
	if m.paths.Phonebooks != "" {
		dir := filepath.Join(m.paths.Phonebooks, accountbook.Dir)
		for path, data := range view {
			if filepath.Dir(path) == dir {
				books[filepath.Base(path)] = data
			}
		}
	}
	if _, problems := configcheck.AccountBooks(books, handsets); len(problems) > 0 {
		return refuse(syncwire.Invalid)
	}
	return nil
}

package syncmachine

import (
	"database/sql"
	"encoding/json"
	"errors"

	"callmemaybe/internal/policy"
	"callmemaybe/internal/provision"
	"callmemaybe/internal/syncwire"
)

func problem(p syncwire.ReceiptProblem) *syncwire.ReceiptProblem { return &p }

// Apply records intent before touching disk. A replay after a crash recognises
// the prepared result's file digest and finishes its receipt without repeating
// the write. Neither a stale web edit nor a failed parse overwrites local work.
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
	var oldBody string
	var oldReceipt, beforeHash, afterHash sql.NullString
	err = m.db.QueryRow("SELECT body,receipt,before_hash,after_hash FROM commands WHERE id=?", item.Id).Scan(&oldBody, &oldReceipt, &beforeHash, &afterHash)
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
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		if _, err = m.db.Exec("INSERT INTO commands (id,body) VALUES (?,?)", item.Id, string(body)); err != nil {
			return result, ErrState
		}
	} else {
		return result, ErrState
	}
	o, err := observe(m.paths)
	if err != nil {
		result.Status, result.Problem = syncwire.ReceiptStatusRejected, problem(syncwire.Invalid)
		return save()
	}
	current, err := m.record(o)
	if err != nil {
		return result, err
	}
	if current.Generation != item.Base.Generation {
		result.Status, result.Problem = syncwire.ReceiptStatusStale, problem(syncwire.BaseChanged)
		return save()
	}
	if afterHash.Valid && o.digest() == afterHash.String {
		result.Status = syncwire.ReceiptStatusSaved
		result.Revision = &current.Revision
		result.Problem = nil
		if m.Applied != nil && m.Applied(m.paths, o.files) {
			result.Status = syncwire.ReceiptStatusApplied
		}
		return save()
	}
	if oldReceipt.Valid || (beforeHash.Valid && o.digest() != beforeHash.String) || current.Revision != item.Base.Revision {
		result.Status, result.Problem = syncwire.ReceiptStatusConflict, problem(syncwire.BaseChanged)
		return save()
	}
	if m.now().UnixMilli() >= item.NotAfter {
		result.Status, result.Problem = syncwire.ReceiptStatusStale, problem(syncwire.Expired)
		return save()
	}
	raw, _ := item.Verb.MarshalJSON()
	var path string
	var after []byte
	switch verbName(raw) {
	case "policy.setSchedule":
		v, err := item.Verb.AsVerb0()
		if err != nil {
			return result, syncwire.ErrInvalid
		}
		path = o.policies[v.Policy]
		if path != "" {
			after, err = patchTable(o.files[path], "schedules", v.Schedule.Id, map[string]any{"start": v.Schedule.Start, "end": v.Schedule.End, "days": v.Schedule.Days, "enabled": v.Schedule.Enabled})
		}
		if path == "" || err != nil {
			result.Problem = problem(syncwire.Invalid)
			return save()
		}
	case "phonebook.assign":
		v, err := item.Verb.AsVerb1()
		if err != nil {
			return result, syncwire.ErrInvalid
		}
		var selected []string
		for _, book := range current.Configuration.Phonebooks {
			if book.Id == v.Phonebook {
				if len(book.Includes) > 0 {
					selected = append(selected, book.Includes...)
				} else {
					selected = []string{book.Id}
				}
			}
		}
		if len(selected) == 0 {
			result.Problem = problem(syncwire.Invalid)
			return save()
		}
		path = m.paths.Handsets
		after, err = patchTable(o.files[path], "handsets", v.Handset, map[string]any{"phonebook": selected})
		if err != nil {
			result.Problem = problem(syncwire.Invalid)
			return save()
		}
	default:
		result.Problem = problem(syncwire.Unsupported)
		return save()
	}
	options := policy.Options{ModelKnown: provision.Known, StrictUnknownKeys: true}
	if data, ok := o.files[m.paths.Trunks]; ok {
		options.Trunks, err = policy.TrunksFromTOML(data)
		if err != nil {
			result.Problem = problem(syncwire.Invalid)
			return save()
		}
	}
	before := o.files[path]
	beforeDigest := o.digest()
	o.files[path] = after
	for _, p := range o.policies {
		if len(policy.LintSplitWith(o.files[p], o.files[m.paths.Handsets], options)) != 0 {
			result.Problem = problem(syncwire.Invalid)
			return save()
		}
	}
	afterDigest := o.digest()
	o.files[path] = before
	if !o.unchanged() {
		result.Status, result.Problem = syncwire.ReceiptStatusConflict, problem(syncwire.BaseChanged)
		return save()
	}
	if _, err = m.db.Exec("UPDATE commands SET target_path=?,before_hash=?,after_hash=? WHERE id=?", path, beforeDigest, afterDigest, item.Id); err != nil {
		return result, ErrState
	}
	if err = atomicReplace(path, before, after); err != nil {
		// Leave prepared intent intact. The next pass determines whether a
		// rename happened, including a directory fsync failure after rename.
		return result, err
	}
	next, err := observe(m.paths)
	if err != nil {
		return result, err
	}
	current, err = m.record(next)
	if err != nil {
		return result, err
	}
	result.Status, result.Revision = syncwire.ReceiptStatusSaved, &current.Revision
	if next.digest() != afterDigest {
		result.Status, result.Problem = syncwire.ReceiptStatusConflict, problem(syncwire.BaseChanged)
	} else if m.Applied != nil && m.Applied(m.paths, next.files) {
		result.Status = syncwire.ReceiptStatusApplied
	}
	return save()
}

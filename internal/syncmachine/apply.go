package syncmachine

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"callmemaybe/internal/configcheck"
	"callmemaybe/internal/policy"
	"callmemaybe/internal/syncwire"
)

func problem(p syncwire.ReceiptProblem) *syncwire.ReceiptProblem { return &p }

func fileHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Apply records intent before touching disk. A replay after a crash recognises
// the target file's written bytes and finishes its receipt without repeating
// the write. Neither a stale web edit nor a failed parse overwrites local work.
// The intent is the target's own hash, not the whole observed set: a contacts
// refresh or an unrelated inventory edit must not turn a landed write into a
// conflict.
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
	var oldReceipt, targetPath, beforeHash, afterHash sql.NullString
	err = m.db.QueryRow("SELECT body,receipt,target_path,before_hash,after_hash FROM commands WHERE id=?", item.Id).Scan(&oldBody, &oldReceipt, &targetPath, &beforeHash, &afterHash)
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
	landed := targetPath.Valid && afterHash.Valid && fileHash(o.files[targetPath.String]) == afterHash.String
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
		result.Status, result.Problem = syncwire.ReceiptStatusStale, problem(syncwire.BaseChanged)
		return save()
	}
	if landed {
		// The process stopped between the rename and the receipt.
		result.Status = syncwire.ReceiptStatusSaved
		result.Revision = &current.Revision
		result.Problem = nil
		if m.Applied != nil && m.Applied(m.paths, o.files) {
			result.Status = syncwire.ReceiptStatusApplied
		}
		return save()
	}
	if (targetPath.Valid && beforeHash.Valid && fileHash(o.files[targetPath.String]) != beforeHash.String) || current.Revision != item.Base.Revision {
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
	// The same rules observe applied, so a change is saved only when the
	// result is a configuration observe — and check, and the daemon — accept.
	options := configcheck.Options(nil)
	if data, ok := o.files[m.paths.Trunks]; ok {
		trunks, err := policy.TrunksFromTOML(data)
		if err != nil {
			result.Problem = problem(syncwire.Invalid)
			return save()
		}
		options = configcheck.Options(trunks)
	}
	before := o.files[path]
	o.files[path] = after
	for _, p := range o.policies {
		if len(policy.LintSplitWith(o.files[p], o.files[m.paths.Handsets], options)) != 0 {
			result.Problem = problem(syncwire.Invalid)
			return save()
		}
	}
	o.files[path] = before
	if !o.unchanged() {
		result.Status, result.Problem = syncwire.ReceiptStatusConflict, problem(syncwire.BaseChanged)
		return save()
	}
	if _, err = m.db.Exec("UPDATE commands SET target_path=?,before_hash=?,after_hash=? WHERE id=?", path, fileHash(before), fileHash(after), item.Id); err != nil {
		return result, ErrState
	}
	renamed, err := atomicReplace(path, before, after)
	if err != nil && renamed {
		// Leave prepared intent intact. The next pass recognises the target's
		// bytes, so a directory fsync failure after the rename is not repeated.
		return result, err
	}
	if err != nil {
		// Nothing replaced the target (a symlink or a directory that refuses a
		// temporary file), so the answer is final. Without a receipt the same
		// item would come back every pass and hold every later command.
		if _, dbErr := m.db.Exec("UPDATE commands SET target_path=NULL,before_hash=NULL,after_hash=NULL WHERE id=?", item.Id); dbErr != nil {
			return result, ErrState
		}
		result.Status, result.Problem = syncwire.ReceiptStatusFailed, problem(syncwire.ExecutionFailed)
		if errors.Is(err, ErrChanged) {
			result.Status, result.Problem = syncwire.ReceiptStatusConflict, problem(syncwire.BaseChanged)
		}
		return save()
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
	if fileHash(next.files[path]) != fileHash(after) {
		result.Status, result.Problem = syncwire.ReceiptStatusConflict, problem(syncwire.BaseChanged)
	} else if m.Applied != nil && m.Applied(m.paths, next.files) {
		result.Status = syncwire.ReceiptStatusApplied
	}
	return save()
}

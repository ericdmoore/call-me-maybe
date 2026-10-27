package syncmachine

import "database/sql"

// AwaitingApply answers, without the network, whether any command's receipt
// is still waiting for the daemon to load what was written. waiting holds
// every command answered saved whose written bytes are on disk and whose
// queue item has not expired; ready holds those of them that Applied proves
// the daemon is running now, so a pass would upgrade them to applied.
//
// A saved receipt is upgraded only by a later pass. With the sync bell up
// passes are minutes apart, so the caller probes this between passes and
// rings for one as soon as an entry turns ready. With nothing waiting it is
// one query of the local ledger and reads no files.
func (m *Machine) AwaitingApply() (waiting, ready []string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows, err := m.db.Query(`SELECT id,target_path,before_hash,after_hash,writes FROM commands
WHERE json_extract(receipt,'$.status')='saved' AND (writes IS NOT NULL OR (target_path IS NOT NULL AND after_hash IS NOT NULL))
AND json_extract(body,'$.notAfter')>? ORDER BY id`, m.now().UnixMilli())
	if err != nil {
		return nil, nil, ErrState
	}
	type pending struct {
		id     string
		writes []write
	}
	var all []pending
	for rows.Next() {
		var p pending
		var target, before, after, writes sql.NullString
		if err = rows.Scan(&p.id, &target, &before, &after, &writes); err == nil {
			p.writes, err = intentOf(target, before, after, writes)
		}
		if err != nil {
			rows.Close()
			return nil, nil, ErrState
		}
		all = append(all, p)
	}
	if rows.Close() != nil || rows.Err() != nil {
		return nil, nil, ErrState
	}
	if len(all) == 0 {
		return nil, nil, nil
	}
	o, err := observe(m.paths)
	if err != nil {
		// Unreadable or mid-rewrite files: still waiting, nothing proven.
		for _, p := range all {
			waiting = append(waiting, p.id)
		}
		return waiting, nil, nil
	}
	applied := m.Applied != nil && m.Applied(m.paths, o.files)
	for _, p := range all {
		// A later local edit replaced the written bytes: Apply will never
		// upgrade it, and the command's own expiry ends the wait.
		landed := true
		for _, w := range p.writes {
			landed = landed && state(o.files, w.Path) == w.After
		}
		if !landed {
			continue
		}
		waiting = append(waiting, p.id)
		if applied {
			ready = append(ready, p.id)
		}
	}
	return waiting, ready, nil
}

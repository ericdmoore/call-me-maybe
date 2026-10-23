package syncmachine

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
	rows, err := m.db.Query(`SELECT id,target_path,after_hash FROM commands
WHERE json_extract(receipt,'$.status')='saved' AND target_path IS NOT NULL AND after_hash IS NOT NULL
AND json_extract(body,'$.notAfter')>? ORDER BY id`, m.now().UnixMilli())
	if err != nil {
		return nil, nil, ErrState
	}
	type pending struct{ id, path, hash string }
	var all []pending
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.id, &p.path, &p.hash); err != nil {
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
		if fileHash(o.files[p.path]) != p.hash {
			continue
		}
		waiting = append(waiting, p.id)
		if applied {
			ready = append(ready, p.id)
		}
	}
	return waiting, ready, nil
}

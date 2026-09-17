package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"callmemaybe/internal/calls"
)

const currentSchema = 3

// This view has no independent state to get out of sync with the commit log.
// A full channel identity, not the old shortened display ID, selects the latest
// summary of the preferred kind if a session is reported twice. CEL records
// root channels only here; linked_id on channel events correlates other legs.
const callsProjection = `
SELECT e.sequence, e.event_id, e.call_id,
 COALESCE(NULLIF(json_extract(e.payload,'$.record.line'),''),'default') AS line,
 COALESCE(NULLIF(json_extract(e.payload,'$.record.direction'),''),'inbound') AS direction,
 json_extract(e.payload,'$.record.outcome') AS outcome,
 COALESCE(json_extract(e.payload,'$.record.caller'),'') AS caller,
 COALESCE(json_extract(e.payload,'$.record.dialled'),'') AS dialled,
 json_extract(e.payload,'$.record') AS record
FROM events e
WHERE e.type IN ('session.finished','call.finished') AND e.call_id!=''
 AND json_type(e.payload,'$.record')='object'
 AND NOT EXISTS (
  SELECT 1 FROM events newer
  WHERE newer.call_id=e.call_id AND newer.type IN ('session.finished','call.finished')
   AND (
   (CASE WHEN json_extract(e.payload,'$.record.direction')='outbound' THEN newer.type='call.finished' ELSE newer.type='session.finished' END) >
   (CASE WHEN json_extract(e.payload,'$.record.direction')='outbound' THEN e.type='call.finished' ELSE e.type='session.finished' END)
   OR ((newer.type=e.type) AND newer.sequence>e.sequence)
  ) AND json_type(newer.payload,'$.record')='object'
 )`

func migrateQueries(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
CREATE INDEX events_type_sequence ON events(type,sequence);
CREATE INDEX events_call_sequence ON events(call_id,sequence);
CREATE INDEX events_line_sequence ON events(line,sequence);
CREATE VIEW public_calls_v1 AS ` + callsProjection + `;
CREATE TABLE cel_progress (singleton INTEGER PRIMARY KEY CHECK(singleton=1), source_id TEXT NOT NULL, through INTEGER NOT NULL, anchor TEXT NOT NULL);
CREATE TABLE cel_channels (id TEXT PRIMARY KEY, state TEXT NOT NULL);
PRAGMA user_version=3;`)
	if err != nil {
		return fmt.Errorf("journal query migration: %w", err)
	}
	return tx.Commit()
}

func readVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, err
	}
	if version < 1 || version > currentSchema {
		return 0, errors.New("unsupported journal schema version")
	}
	return version, nil
}

func ValidDirection(s string) bool {
	return s == "" || s == calls.DirectionInbound || s == calls.DirectionOutbound
}

// CallHistory is a human-facing projection. The event page is the consumer
// replay contract; call summaries can change when a later summary arrives.
type CallHistory struct {
	Records        []calls.Record
	RetentionFloor int64
	GapEvents      int64
}

func ReadCalls(ctx context.Context, path string, f calls.Filter, full bool) (CallHistory, error) {
	h := CallHistory{Records: []calls.Record{}}
	if f.Limit < 0 || !ValidDirection(f.Direction) {
		return h, errors.New("invalid call limit or direction")
	}
	db, err := connect(path, true)
	if err != nil {
		return h, err
	}
	defer db.Close()
	version, err := readVersion(ctx, db)
	if err != nil {
		return h, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return h, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, "SELECT floor FROM metadata WHERE singleton=1").Scan(&h.RetentionFloor); err != nil {
		return h, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM events WHERE type=?", CoverageGap).Scan(&h.GapEvents); err != nil {
		return h, err
	}
	query := "SELECT record FROM public_calls_v1"
	if version < 3 { // Offline reads of an older journal must not migrate it.
		query = "WITH public_calls_v1 AS (" + callsProjection + ") " + query
	}
	var where []string
	var args []any
	for _, filter := range []struct{ column, value string }{{"direction", f.Direction}, {"line", f.Line}, {"outcome", f.Outcome}} {
		if filter.value != "" {
			where = append(where, filter.column+"=?")
			args = append(args, filter.value)
		}
	}
	if f.Caller != "" {
		where = append(where, "(instr(caller,?)>0 OR instr(dialled,?)>0)")
		args = append(args, f.Caller, f.Caller)
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY sequence DESC"
	// Since is checked as a Go instant, preserving nanosecond and timezone
	// semantics. Apply the limit after that check, not before it.
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return h, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var r calls.Record
		if err = rows.Scan(&raw); err != nil {
			return h, err
		}
		if err = json.Unmarshal([]byte(raw), &r); err != nil {
			return h, err
		}
		if !f.Matches(r) {
			continue
		}
		if !full {
			r = r.Redacted()
		}
		h.Records = append(h.Records, r)
		if f.Limit > 0 && len(h.Records) == f.Limit {
			break
		}
	}
	if err = rows.Err(); err != nil {
		return h, err
	}
	// Match the legacy display: newest N records, presented oldest-first.
	for i, j := 0, len(h.Records)-1; i < j; i, j = i+1, j-1 {
		h.Records[i], h.Records[j] = h.Records[j], h.Records[i]
	}
	return h, nil
}

package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"callmemaybe/internal/calls"
)

const currentSchema = 4

// This view has no independent state to get out of sync with the commit log.
// A full channel identity, not the old shortened display ID, selects the latest
// summary of the preferred kind if a session is reported twice. CEL records
// root channels only here; linked_id on channel events correlates other legs.
const callsProjection = `
WITH summaries AS (
 SELECT e.sequence,e.event_id,e.call_id,
 CASE WHEN e.type='session.finished' AND json_extract(e.payload,'$.record.direction')='outbound'
 THEN json_set(json_extract(e.payload,'$.record'),'$.ms',COALESCE(
 (SELECT json_extract(c.payload,'$.record.ms') FROM events c WHERE c.call_id=e.call_id AND c.type='call.finished' ORDER BY c.sequence DESC LIMIT 1),json_extract(e.payload,'$.record.ms')))
 ELSE json_extract(e.payload,'$.record') END AS record
 FROM events e
 WHERE e.type IN ('session.finished','call.finished') AND e.call_id!='' AND json_type(e.payload,'$.record')='object'
 AND NOT EXISTS (SELECT 1 FROM events newer WHERE newer.call_id=e.call_id
 AND newer.type IN ('session.finished','call.finished') AND json_type(newer.payload,'$.record')='object'
 AND ((newer.type='session.finished' AND e.type='call.finished') OR (newer.type=e.type AND newer.sequence>e.sequence)))
)
SELECT sequence,event_id,call_id,
 COALESCE(NULLIF(json_extract(record,'$.line'),''),'default') AS line,
 COALESCE(NULLIF(json_extract(record,'$.direction'),''),'inbound') AS direction,
 json_extract(record,'$.outcome') AS outcome,
 COALESCE(json_extract(record,'$.caller'),'') AS caller,
 COALESCE(json_extract(record,'$.dialled'),'') AS dialled,record FROM summaries`

const celSchema = `
CREATE TABLE cel_progress (singleton INTEGER PRIMARY KEY CHECK(singleton=1), source_id TEXT NOT NULL, through INTEGER NOT NULL, anchor TEXT NOT NULL);
CREATE TABLE cel_channels (id TEXT PRIMARY KEY, state TEXT NOT NULL);`

func migrateQueries(db *sql.DB, version int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ddl := ""
	if version < 2 {
		ddl += `CREATE INDEX events_type_sequence ON events(type,sequence);CREATE INDEX events_call_sequence ON events(call_id,sequence);CREATE INDEX events_line_sequence ON events(line,sequence);`
	}
	if version < 3 {
		ddl += celSchema
	}
	ddl += `DROP VIEW IF EXISTS public_calls_v1; CREATE VIEW public_calls_v1 AS ` + callsProjection + `; PRAGMA user_version=4;`
	if _, err = tx.Exec(ddl); err != nil {
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
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM events WHERE type=? AND COALESCE(json_extract(payload,'$.reason'),'') NOT IN ('cel-observation-started; prior system coverage unknown','writer-recovered','cel-ingestion-unavailable; replay pending')", CoverageGap).Scan(&h.GapEvents); err != nil {
		return h, err
	}
	query := "SELECT record FROM public_calls_v1"
	if version < currentSchema { // Offline reads of an older journal must not migrate it.
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
	// SQL is a prefilter; Matches preserves the shared legacy filter semantics.
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
	slices.Reverse(h.Records)
	return h, nil
}

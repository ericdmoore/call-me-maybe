package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"callmemaybe/internal/calls"
	"modernc.org/sqlite"
)

const schemaSQL = `
CREATE TABLE metadata (singleton INTEGER PRIMARY KEY CHECK(singleton=1), journal_id TEXT NOT NULL, generation TEXT NOT NULL, high INTEGER NOT NULL DEFAULT 0, floor INTEGER NOT NULL DEFAULT 0, clean INTEGER NOT NULL DEFAULT 1);
CREATE TABLE events (sequence INTEGER PRIMARY KEY AUTOINCREMENT, event_id TEXT NOT NULL UNIQUE, type TEXT NOT NULL, version INTEGER NOT NULL, occurred_at TEXT NOT NULL, recorded_at TEXT NOT NULL, source TEXT NOT NULL, call_id TEXT NOT NULL, line TEXT NOT NULL, payload TEXT NOT NULL);
CREATE INDEX events_recorded_at ON events(recorded_at,sequence);
CREATE TABLE delivery_state (target TEXT PRIMARY KEY, through INTEGER NOT NULL);
CREATE VIEW public_events_v1 AS SELECT e.*, m.journal_id, m.generation FROM events e CROSS JOIN metadata m;
PRAGMA user_version=1;
`

type store struct {
	db   *sql.DB
	path string
	opts Options
}

func connect(path string, readonly bool, pragmas ...string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := u.Query()
	if readonly {
		q.Set("mode", "ro")
	} else {
		q.Set("mode", "rw")
		q.Set("_txlock", "immediate")
	}
	q.Add("_pragma", "busy_timeout(250)")
	if readonly {
		q.Add("_pragma", "query_only(1)")
	}
	for _, pragma := range pragmas {
		q.Add("_pragma", pragma)
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}
func openStore(path string, opts Options) (_ *store, err error) {
	// A private directory also protects the WAL, SHM, and owner lock. Do not chmod
	// an existing shared directory: refuse it so we cannot change unrelated files.
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("journal requires a private directory (0700)")
	}
	if info, e := os.Lstat(path); e == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		return nil, errors.New("journal requires a regular private file (0600)")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	db, err := connect(path, false)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = db.Close()
		}
	}()
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if version < 0 || version > currentSchema {
		return nil, errors.New("unsupported journal schema version")
	}
	if version == 0 {
		var count int
		if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&count); err != nil {
			return nil, err
		}
		if count != 0 {
			return nil, errors.New("refusing to initialise an unrelated database")
		}
		tx, e := db.Begin()
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		if _, err = tx.Exec(schemaSQL); err != nil {
			return nil, err
		}
		if _, err = tx.Exec("INSERT INTO metadata(singleton,journal_id,generation) VALUES(1,?,?)", identity(), identity()); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
	}
	if version < currentSchema {
		if err = migrateQueries(db, version); err != nil {
			return nil, err
		}
	}
	var pageSize int64
	if err = db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		return nil, err
	}
	var pageCount int64
	if err = db.QueryRow("PRAGMA page_count").Scan(&pageCount); err != nil {
		return nil, err
	}
	if pageCount*pageSize > opts.MaxBytes/4 {
		return nil, errors.New("existing journal exceeds database budget; archive and compact it before lowering the limit")
	}
	// Close the bootstrap connection: every future connection gets the budget.
	if err = db.Close(); err != nil {
		return nil, err
	}
	db, err = connect(path, false, "journal_mode(WAL)", "synchronous(FULL)", "wal_autocheckpoint(64)", "journal_size_limit(0)", fmt.Sprintf("max_page_count(%d)", opts.MaxBytes/4/pageSize))
	if err != nil {
		return nil, err
	}
	if err = db.Ping(); err != nil {
		return nil, err
	}
	return &store{db: db, path: path, opts: opts}, nil
}
func (s *store) append(ctx context.Context, e Event) error {
	if !ValidType(e.Type) {
		return errors.New("unknown event type")
	}
	if e.ID == "" {
		e.ID = identity()
	}
	e.Version = 1
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	if err := s.walBudget(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.appendTx(ctx, tx, e); err != nil {
		return err
	}
	return tx.Commit()
}

// appendTx lets source progress and derived events commit as one unit.
func (s *store) appendTx(ctx context.Context, tx *sql.Tx, e Event) error {
	if e.ID == "" {
		e.ID = identity()
	}
	e.Version = 1
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	e.RecordedAt = time.Now().UTC()
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return err
	}
	if len(payload) > 32768 {
		return errors.New("event payload exceeds 32 KiB")
	}
	if err = s.prune(ctx, tx, 1); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO events(event_id,type,version,occurred_at,recorded_at,source,call_id,line,payload) VALUES(?,?,?,?,?,?,?,?,?)`, e.ID, e.Type, e.Version, e.At.Format(time.RFC3339Nano), e.RecordedAt.Format("2006-01-02T15:04:05.000000000Z07:00"), e.Source, e.CallID, e.Line, string(payload))
	if err != nil {
		return err
	}
	seq, err := result.LastInsertId()
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE metadata SET high=? WHERE singleton=1", seq); err != nil {
		return err
	}
	return nil
}
func (s *store) walBudget(ctx context.Context) error {
	// Reserve a quarter of the total budget for database pages and room for
	// a whole-database transaction in the WAL, including frame overhead.
	// External long-running readers can pin WAL pages. Stop growing it rather
	// than let observation consume the disk the phone needs.
	if st, err := os.Stat(s.path + "-wal"); err == nil && st.Size() > s.opts.MaxBytes/2-s.opts.MaxBytes/128-(1<<20) {
		_, _ = s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
		if st, err = os.Stat(s.path + "-wal"); err == nil && st.Size() > s.opts.MaxBytes/2-s.opts.MaxBytes/128-(1<<20) {
			return errors.New("journal WAL budget exhausted")
		}
	}
	return nil
}
func (s *store) prune(ctx context.Context, tx *sql.Tx, reserve int) error {
	// Delete only a prefix: floor is a truthful replay boundary, never a promise
	// that an internal sequence gap represents an event a consumer can recover.
	var floor int64
	err := tx.QueryRowContext(ctx, `SELECT MAX(boundary) FROM (
 SELECT COALESCE(MAX(sequence),0) AS boundary FROM events
 WHERE sequence < (SELECT COALESCE(MIN(sequence),(SELECT high+1 FROM metadata)) FROM events WHERE recorded_at >= ?)
 UNION ALL SELECT COALESCE(MAX(sequence),0) FROM events WHERE sequence <= (SELECT high-? FROM metadata)
)`, time.Now().UTC().Add(-s.opts.MaxAge).Format("2006-01-02T15:04:05.000000000Z07:00"), s.opts.MaxEvents-reserve).Scan(&floor)
	if err != nil {
		return err
	}
	if floor == 0 {
		return nil
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM events WHERE sequence<=?", floor); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE metadata SET floor=MAX(floor,?)", floor)
	return err
}

// reclaim handles the physical page budget even when rows are unusually large.
func (s *store) reclaim(ctx context.Context) error {
	if err := s.walBudget(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var floor int64
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM (SELECT sequence FROM events ORDER BY sequence LIMIT 256)").Scan(&floor); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM events WHERE sequence<=?", floor); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE metadata SET floor=MAX(floor,?)", floor); err != nil {
		return err
	}
	return tx.Commit()
}

type Page struct {
	JournalID         string  `json:"journal_id"`
	Generation        string  `json:"generation"`
	Events            []Event `json:"events"`
	NextCursor        int64   `json:"next_cursor,string"`
	EarliestAvailable int64   `json:"earliest_available,string"`
	HighWatermark     int64   `json:"high_watermark,string"`
	RetentionFloor    int64   `json:"retention_floor,string"`
	Through           int64   `json:"through,string"`
}
type Query struct {
	After                   int64
	Limit                   int
	EventType               Type
	JournalID, Generation   string
	Full                    bool
	Direction, Line, CallID string
	Through                 *int64 // nil captures the current high watermark; zero is an explicit empty batch.
}

func Read(ctx context.Context, path string, q Query) (Page, error) {
	p := Page{Events: []Event{}}
	if q.After < 0 || q.Limit < 1 || q.Limit > 10000 || (q.Through != nil && (*q.Through < 0 || *q.Through < q.After)) {
		return p, errors.New("after must be nonnegative, limit must be 1–10000, and through must not precede after")
	}
	if !ValidDirection(q.Direction) {
		return p, errors.New("direction must be inbound or outbound")
	}
	if q.EventType != "" && !ValidType(q.EventType) {
		return p, errors.New("unknown eventType; see doorman events --help")
	}
	db, err := connect(path, true)
	if err != nil {
		return p, err
	}
	defer db.Close()
	if _, err = readVersion(ctx, db); err != nil {
		return p, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, "SELECT journal_id,generation,high,floor FROM metadata WHERE singleton=1").Scan(&p.JournalID, &p.Generation, &p.HighWatermark, &p.RetentionFloor); err != nil {
		return p, err
	}
	if q.JournalID != "" && q.JournalID != p.JournalID || q.Generation != "" && q.Generation != p.Generation {
		return p, errors.New("journal identity or generation mismatch")
	}
	if q.After != 0 && q.After < p.RetentionFloor {
		return p, errors.New("cursor expired: resume explicitly from 0 or restore archived history")
	}
	if q.After > p.HighWatermark {
		return p, errors.New("cursor is ahead of this journal")
	}
	p.Through = p.HighWatermark
	if q.Through != nil {
		p.Through = *q.Through
	}
	if p.Through > p.HighWatermark {
		return p, errors.New("through is ahead of this journal")
	}
	if p.Through < p.RetentionFloor && p.Through != 0 {
		return p, errors.New("batch expired: through precedes the retained history")
	}
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MIN(sequence),0) FROM public_events_v1").Scan(&p.EarliestAvailable); err != nil {
		return p, err
	}
	if q.Through != nil && *q.Through == 0 {
		return p, nil
	}
	after := q.After
	if after < p.RetentionFloor {
		after = p.RetentionFloor
	}
	// Bound the scan first, then filter in SQL. This preserves cursor progress
	// even for a sparse filter and never jumps over an unreturned match.
	var end int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),?) FROM
      (SELECT sequence FROM public_events_v1 WHERE sequence>? AND sequence<=? ORDER BY sequence LIMIT 10000)`, after, after, p.Through).Scan(&end); err != nil {
		return p, err
	}
	conditions := []string{"sequence>?", "sequence<=?"}
	args := []any{after, end}
	if q.EventType != "" {
		conditions = append(conditions, "type=?")
		args = append(args, q.EventType)
	}
	if q.CallID != "" {
		conditions = append(conditions, "call_id=?")
		args = append(args, q.CallID)
	}
	if q.Line != "" {
		conditions = append(conditions, "(line=? OR (?='default' AND line='' AND call_id!=''))")
		args = append(args, q.Line, q.Line)
	}
	if q.Direction != "" {
		conditions = append(conditions, "COALESCE(json_extract(payload,'$.channel.direction'), CASE WHEN json_type(payload,'$.record')='object' THEN COALESCE(NULLIF(json_extract(payload,'$.record.direction'),''),'inbound') END)=?")
		args = append(args, q.Direction)
	}
	args = append(args, q.Limit)
	rows, err := tx.QueryContext(ctx, `SELECT sequence,event_id,type,version,occurred_at,recorded_at,source,call_id,line,payload FROM public_events_v1 WHERE `+strings.Join(conditions, " AND ")+` ORDER BY sequence LIMIT ?`, args...)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	p.NextCursor = after
	for rows.Next() {
		var e Event
		var at, recorded, payload string
		if err = rows.Scan(&e.Sequence, &e.ID, &e.Type, &e.Version, &at, &recorded, &e.Source, &e.CallID, &e.Line, &payload); err != nil {
			return p, err
		}
		if e.At, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return p, err
		}
		if e.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded); err != nil {
			return p, err
		}
		if err = json.Unmarshal([]byte(payload), &e.Payload); err != nil {
			return p, err
		}
		p.NextCursor = e.Sequence
		if !q.Full && e.Payload.Record != nil {
			r := e.Payload.Record.Redacted()
			e.Payload.Record = &r
		}
		if !q.Full && e.Payload.Channel != nil {
			c := *e.Payload.Channel
			r := calls.Record{Caller: c.Caller, Dialled: c.Dialled}.Redacted()
			c.Caller, c.Dialled = r.Caller, r.Dialled
			e.Payload.Channel = &c
		}
		p.Events = append(p.Events, e)
		if len(p.Events) == q.Limit {
			break
		}
	}
	if err = rows.Err(); err != nil {
		return p, err
	}
	if len(p.Events) < q.Limit {
		p.NextCursor = end
	}
	if end == after && len(p.Events) == 0 {
		p.NextCursor = p.Through
	}
	return p, nil
}

func retryBusy(ctx context.Context, op func() error) error {
	for attempt := 0; ; attempt++ {
		err := op()
		var se *sqlite.Error
		if !errors.As(err, &se) || se.Code()&255 != 5 || attempt >= 4 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 20 * time.Millisecond):
		}
	}
}
func (s *store) appendRecover(ctx context.Context, e Event) error {
	return retryBusy(ctx, func() error {
		err := s.append(ctx, e)
		var se *sqlite.Error
		if errors.As(err, &se) && se.Code()&255 == 13 {
			if s.reclaim(ctx) == nil {
				return s.append(ctx, e)
			}
		}
		return err
	})
}

// CheckPath validates an existing journal without creating one or acquiring the
// writer lock. Missing storage is fine at setup time; the daemon creates it.
func CheckPath(path string, options ...Options) error {
	o := defaults(Options{})
	if len(options) > 0 {
		o = defaults(options[0])
	}
	if info, err := os.Stat(filepath.Dir(path)); err == nil {
		if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return errors.New("journal parent directory must be private (0700)")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("journal must be a regular private file (0600)")
	}
	if info.Size() > o.MaxBytes/4 {
		return errors.New("existing journal exceeds database budget; archive and compact it before lowering the limit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = Read(ctx, path, Query{Limit: 1})
	if err != nil {
		return err
	}
	db, err := connect(path, true)
	if err != nil {
		return err
	}
	defer db.Close()
	var pages, size int64
	if err = db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return err
	}
	if err = db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&size); err != nil {
		return err
	}
	if pages*size > o.MaxBytes/4 {
		return errors.New("existing journal exceeds database budget; archive and compact it before lowering the limit")
	}
	return nil
}

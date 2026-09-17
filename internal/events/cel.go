package events

// Asterisk owns the CEL spool. Doorman reads bounded snapshots and commits
// source progress, channel state, and public observations together. None of
// this code runs on an ARI or dialplan call-control path.
import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"callmemaybe/internal/calls"
	"callmemaybe/internal/policy"
	"modernc.org/sqlite"
)

// ChannelObservation is deliberately smaller than CEL. No application data,
// user fields, channel names (which can embed destinations), or entered digits.
type celRow struct {
	seq                                                     int64
	kind, at, id, linked, context, caller, destination, app string
}
type celState struct {
	Record  calls.Record `json:"record"`
	Tracked bool         `json:"tracked"`
	Bridged bool         `json:"bridged"`
	Seen    time.Time    `json:"seen"`
}

var phoneDestination = regexp.MustCompile(`^(?:[2-9][0-9]{2}[2-9][0-9]{6}|1[2-9][0-9]{2}[2-9][0-9]{6}|911)$`)
var safeCELIdentifier = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

// CheckCELPath checks the spool contract without creating or changing it.
func CheckCELPath(ctx context.Context, path string) error {
	db, err := connect(path, true)
	if err != nil {
		return err
	}
	defer db.Close()
	if err = checkCELSequence(ctx, db); err != nil {
		return err
	}
	var id string
	if err = db.QueryRowContext(ctx, "SELECT source_id FROM doorman_cel_meta WHERE singleton=1 AND version=1").Scan(&id); err != nil {
		return fmt.Errorf("read CEL source identity: %w", err)
	}
	if !safeCELIdentifier.MatchString(id) {
		return errors.New("invalid CEL source identity")
	}
	rows, err := db.QueryContext(ctx, "SELECT AcctId,eventtype,eventtime,uniqueid,linkedid,context,caller,destination,app FROM doorman_cel LIMIT 0")
	if err != nil {
		return fmt.Errorf("read CEL spool columns: %w", err)
	}
	return rows.Close()
}

func readCEL(ctx context.Context, path string, after int64, previous, anchor string) (string, int64, int64, []celRow, error) {
	db, err := connect(path, true)
	if err != nil {
		return "", 0, 0, nil, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", 0, 0, nil, err
	}
	defer tx.Rollback()
	if err = checkCELSequence(ctx, tx); err != nil {
		return "", 0, 0, nil, err
	}
	var source string
	var low, high int64
	if err = tx.QueryRowContext(ctx, "SELECT source_id FROM doorman_cel_meta WHERE singleton=1 AND version=1").Scan(&source); err != nil {
		return "", 0, 0, nil, err
	}
	if !safeCELIdentifier.MatchString(source) {
		return "", 0, 0, nil, errors.New("invalid CEL identity")
	}
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MIN(AcctId),0),COALESCE(MAX(AcctId),0) FROM doorman_cel").Scan(&low, &high); err != nil {
		return "", 0, 0, nil, err
	}
	// A restored spool that reused AcctIds must not silently skip new rows.
	if previous == source && after >= low && after > 0 && anchor != "" {
		var r celRow
		err = tx.QueryRowContext(ctx, "SELECT AcctId,eventtype,eventtime,uniqueid,linkedid,context,caller,destination,app FROM doorman_cel WHERE AcctId=?", after).Scan(&r.seq, &r.kind, &r.at, &r.id, &r.linked, &r.context, &r.caller, &r.destination, &r.app)
		if err != nil || celAnchor(r) != anchor {
			return "", 0, 0, nil, errors.New("CEL source anchor changed; initialise a new source identity")
		}
	}
	// The caller retries from zero after committing a changed source identity.
	rows, err := tx.QueryContext(ctx, "SELECT AcctId,eventtype,eventtime,uniqueid,linkedid,context,caller,destination,app FROM doorman_cel WHERE AcctId>? ORDER BY AcctId LIMIT 128", after)
	if err != nil {
		return "", 0, 0, nil, err
	}
	defer rows.Close()
	var result []celRow
	for rows.Next() {
		var r celRow
		if err = rows.Scan(&r.seq, &r.kind, &r.at, &r.id, &r.linked, &r.context, &r.caller, &r.destination, &r.app); err != nil {
			return "", 0, 0, nil, err
		}
		result = append(result, r)
	}
	return source, low, high, result, rows.Err()
}

func celTime(s string) (time.Time, error) {
	sec, frac, ok := strings.Cut(s, ".")
	if !ok || len(frac) != 6 {
		return time.Time{}, errors.New("CEL eventtime must be epoch seconds.microseconds")
	}
	seconds, e := strconv.ParseInt(sec, 10, 64)
	if e != nil {
		return time.Time{}, e
	}
	micro, e := strconv.ParseInt(frac, 10, 64)
	if e != nil || micro < 0 || micro > 999999 {
		return time.Time{}, errors.New("invalid CEL microseconds")
	}
	return time.Unix(seconds, micro*1000).UTC(), nil
}
func celType(r celRow) Type {
	switch r.kind {
	case "CHAN_START":
		return ChannelStarted
	case "ANSWER":
		return ChannelAnswered
	case "HANGUP":
		return ChannelHungup
	case "CHAN_END":
		return ChannelEnded
	case "BRIDGE_ENTER":
		return ChannelBridgeEntered
	case "BRIDGE_EXIT":
		return ChannelBridgeExited
	case "BLINDTRANSFER", "ATTENDEDTRANSFER":
		return ChannelTransfer
	case "LINKEDID_END":
		return ChannelLinkedEnded
	case "APP_START":
		if strings.EqualFold(r.app, "Dial") {
			return ChannelDialStarted
		}
		return ChannelApplication
	default:
		return ""
	}
}
func inboundLine(ctx string) (string, bool) {
	if ctx == "inbound-trunk" || ctx == "inbound-fallback" {
		return "", true
	}
	for _, p := range []string{"cmm-line-", "from-trunk-"} {
		if strings.HasPrefix(ctx, p) {
			name := strings.TrimPrefix(ctx, p)
			if name == "default" {
				name = ""
			}
			return name, true
		}
	}
	if strings.HasPrefix(ctx, "from-") {
		return "unknown", true
	}
	return "", false
}
func outboundContext(ctx string) bool {
	return ctx == "internal" || ctx == "cmm-outbound" || ctx == "outbound-console" || ctx == "cmm-emergency"
}
func celEventID(source string, seq int64, suffix string) string {
	return digest(fmt.Sprintf("cel:%s:%d:%s", source, seq, suffix))
}

// ingestCEL is called only by the journal's single writer. A failed transaction
// leaves its source cursor unchanged, including when the journal is full.
func (s *store) ingestCEL(ctx context.Context, path string) error {
	var previous, anchor string
	var after int64
	err := s.db.QueryRowContext(ctx, "SELECT source_id,through,anchor FROM cel_progress WHERE singleton=1").Scan(&previous, &after, &anchor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	source, low, high, rows, err := readCEL(ctx, path, after, previous, anchor)
	if err != nil {
		return err
	}
	if source == previous && high == after && len(rows) == 0 {
		return nil
	}
	if err = s.walBudget(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	gap := ""
	count := int64(0)
	if previous != "" && source != previous {
		gap = "cel-source-replaced"
		after = 0
		anchor = ""
		rows = nil
	} else if source == previous && high < after {
		return errors.New("CEL spool rewound under the same source identity; preserve it and initialise a new source")
	}
	if low > after+1 {
		if gap != "" {
			if err = s.appendTx(ctx, tx, System(CoverageGap, gap, 0)); err != nil {
				return err
			}
		}
		gap = "cel-source-retention-gap"
		count = low - after - 1
		after = low - 1
		anchor = ""
	}
	if previous == "" && low <= 1 {
		gap = "cel-observation-started; prior system coverage unknown"
	}
	if gap != "" {
		if _, err = tx.ExecContext(ctx, "DELETE FROM cel_channels"); err != nil {
			return err
		}
		kind := CoverageGap
		if strings.HasPrefix(gap, "cel-observation-started") {
			kind = JournalNote
		}
		if err = s.appendTx(ctx, tx, System(kind, gap, count)); err != nil {
			return err
		}
	}
	// Stale/incomplete channels must not grow forever if Asterisk lost an end
	// event. This is a coverage notice, never a fabricated hangup.
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, "DELETE FROM cel_channels WHERE json_extract(state,'$.seen')<?", cutoff)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		if err = s.appendTx(ctx, tx, System(CoverageGap, "cel-stale-channel-state", n)); err != nil {
			return err
		}
	}
	for _, row := range rows {
		if row.seq <= after {
			continue
		}
		if row.seq != after+1 {
			if err = s.appendTx(ctx, tx, System(CoverageGap, "cel-source-sequence-gap", row.seq-after-1)); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "DELETE FROM cel_channels"); err != nil {
				return err
			}
		}
		if err = s.ingestCELRow(ctx, tx, source, row); err != nil {
			return err
		}
		after = row.seq
		anchor = celAnchor(row)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO cel_progress VALUES(1,?,?,?) ON CONFLICT(singleton) DO UPDATE SET source_id=excluded.source_id,through=excluded.through,anchor=excluded.anchor", source, after, anchor); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *store) ingestCELRow(ctx context.Context, tx *sql.Tx, source string, row celRow) error {
	kind := celType(row)
	at, err := celTime(row.at)
	if kind == "" || err != nil || !safeCELIdentifier.MatchString(row.id) || !safeCELIdentifier.MatchString(row.linked) {
		return s.appendTx(ctx, tx, System(CoverageGap, "cel-invalid-or-unsupported-row", 1))
	}
	var st celState
	var raw string
	err = tx.QueryRowContext(ctx, "SELECT state FROM cel_channels WHERE id=?", row.id).Scan(&raw)
	if err == nil {
		if err = json.Unmarshal([]byte(raw), &st); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	fresh := errors.Is(err, sql.ErrNoRows)
	if fresh {
		st.Record = calls.Record{ID: row.id, Start: at, Line: "unknown"}
		st.Seen = time.Now().UTC()
	}
	// Only root dialplan channels become call summaries; individual handset and
	// trunk legs still have public channel events, linked by linked_id.
	if line, ok := inboundLine(row.context); ok && row.id == row.linked && (row.kind == "CHAN_START" || row.kind == "APP_START") {
		st.Tracked = true
		st.Record.Line = line
		st.Record.Direction = calls.DirectionInbound
		st.Record.Caller = policy.E164OrEmpty(row.caller, s.opts.DefaultCountryCode)
	}
	if !st.Tracked && row.id == row.linked && row.kind == "APP_START" && strings.EqualFold(row.app, "Dial") && outboundContext(row.context) && phoneDestination.MatchString(row.destination) {
		st.Tracked = true
		st.Record.Direction = calls.DirectionOutbound
		st.Record.Dialled = row.destination
		st.Record.Line = "unknown"
		// Start at the Dial attempt, excluding time spent in the *4 console.
		st.Record.Start = at
	}
	if st.Tracked && kind == ChannelBridgeEntered {
		st.Bridged = true
	}
	c := ChannelObservation{SourceID: source, SourceSequence: row.seq, LinkedID: row.linked}
	if st.Tracked {
		c.Direction = st.Record.Direction
		c.Caller = st.Record.Caller
		c.Dialled = st.Record.Dialled
	}
	e := Event{ID: celEventID(source, row.seq, "channel"), Type: kind, At: at, Source: "asterisk.cel", CallID: row.id, Line: st.Record.Line, Payload: Payload{Channel: &c}}
	if err = s.appendTx(ctx, tx, e); err != nil {
		return err
	}
	if kind == ChannelEnded || kind == ChannelLinkedEnded {
		if kind == ChannelEnded && st.Tracked {
			st.Record.MS = at.Sub(st.Record.Start).Milliseconds()
			if st.Record.MS < 0 {
				st.Record.MS = 0
			}
			st.Record.Outcome = calls.OutcomeEnded
			st.Record.Reason = "asterisk-channel-ended; no bridge observed"
			if st.Bridged {
				st.Record.Reason = "asterisk-channel-ended; bridge observed, inbound answer not inferred"
			}
			if st.Bridged && st.Record.Direction == calls.DirectionOutbound {
				st.Record.Outcome = calls.OutcomeAnswered
				st.Record.Reason = "asterisk-bridge-observed"
			}
			e.ID = celEventID(source, row.seq, "summary")
			e.Type = CallFinished
			e.Payload = Payload{Record: &st.Record, Channel: &c}
			if err = s.appendTx(ctx, tx, e); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM cel_channels WHERE id=?", row.id)
		return err
	}
	if fresh {
		var n int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM cel_channels").Scan(&n); err != nil {
			return err
		}
		if n >= 4096 {
			return s.appendTx(ctx, tx, System(CoverageGap, "cel-channel-state-capacity", 1))
		}
	}
	encoded, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO cel_channels VALUES(?,?) ON CONFLICT(id) DO UPDATE SET state=excluded.state", row.id, string(encoded))
	return err
}

func celAnchor(r celRow) string {
	return digest(fmt.Sprintf("%q", []string{r.kind, r.at, r.id, r.linked, r.context, r.caller, r.destination, r.app}))
}

func (s *store) ingestCELRecover(ctx context.Context, path string) error {
	err := s.ingestCEL(ctx, path)
	var se *sqlite.Error
	if errors.As(err, &se) && se.Code()&255 == 13 {
		if s.reclaim(ctx) == nil {
			return s.ingestCEL(ctx, path)
		}
	}
	return err
}

// The backend's automatically created table lacks AUTOINCREMENT. Requiring
// our initialisation prevents source sequence reuse after retention/deletion.
func checkCELSequence(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) error {
	var definition string
	if err := q.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name='doorman_cel'").Scan(&definition); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("CEL spool table missing; initialise with scripts/cel-spool.sql")
		}
		return fmt.Errorf("read CEL spool schema: %w", err)
	}
	if !strings.Contains(strings.ToUpper(definition), "AUTOINCREMENT") {
		return errors.New("CEL spool requires monotonic AUTOINCREMENT source IDs")
	}
	return nil
}

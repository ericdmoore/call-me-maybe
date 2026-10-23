package syncmachine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"callmemaybe/internal/syncwire"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type Machine struct {
	mu    sync.Mutex
	db    *sql.DB
	lock  *os.File
	paths Paths
	now   func() time.Time
	// Applied must prove that the daemon accepted this exact file revision.
	// nil keeps an honest saved receipt until a later run can confirm it.
	Applied      func(Paths, map[string][]byte) bool
	SampleStatus func() syncwire.Status
	// BoxName is the name the box suggests (config.BoxName). The status report
	// carries it until the service has accepted one report with it for this
	// enrolment; empty never sends one. Never logged.
	BoxName string
	// Warn receives categorical problems that do not stop a pass. nil prints
	// them to stderr.
	Warn func(error)

	// rejection is the newest content's validation problem while it is
	// refused, and rejectedKey identifies that content so it is logged once.
	// Guarded by mu: the watcher and Exchange both observe.
	rejection   *syncwire.ConfigRejection
	rejectedKey string

	// Exchange-only state; Exchange never runs concurrently with itself.
	published   string
	publishedAt time.Time
	warned      map[string]string
	// nameAccepted mirrors the box_name row for this enrolment; nameRetryAt
	// pauses the suggestion after a refused report (see reportStatus).
	nameAccepted bool
	nameRetryAt  time.Time

	box string
}

// Open keeps the ledger scoped to the enrolled device. It belongs in state/sync,
// which backups exclude: a replacement signs in and starts its own generation.
func Open(dir, box string, paths Paths) (*Machine, error) {
	if _, err := uuid.Parse(box); err != nil {
		return nil, ErrState
	}
	if os.MkdirAll(dir, 0o700) != nil {
		return nil, ErrState
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, ErrState
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, ErrState
	}
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		lock.Close()
		return nil, errors.New("sync: another sync process is running")
	}
	fail := func() (*Machine, error) { lock.Close(); return nil, ErrState }
	path := filepath.Join(dir, "echo.db")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fail()
	}
	info, err = f.Stat()
	f.Close()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return fail()
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(1000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return fail()
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS echo (singleton INTEGER PRIMARY KEY CHECK(singleton=1), box TEXT NOT NULL, generation TEXT NOT NULL, revision INTEGER NOT NULL, configuration TEXT NOT NULL, observed_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS commands (id TEXT PRIMARY KEY, body TEXT NOT NULL, receipt TEXT, target_path TEXT, before_hash TEXT, after_hash TEXT);
CREATE TABLE IF NOT EXISTS revisions (revision INTEGER PRIMARY KEY, box_digest TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS box_name (box TEXT PRIMARY KEY, accepted_at INTEGER NOT NULL);
`)
	// writes is the intent of a command that changes several files: a JSON
	// list of {path, before, after}. Single-file rows from earlier releases
	// keep target_path/before_hash/after_hash and are read as one write.
	var hasWrites int
	if err == nil {
		err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('commands') WHERE name='writes'").Scan(&hasWrites)
	}
	if err == nil && hasWrites == 0 {
		_, err = db.Exec("ALTER TABLE commands ADD COLUMN writes TEXT")
	}
	if err == nil {
		_, err = db.Exec("INSERT OR IGNORE INTO echo VALUES (1,?,?,0,'',0)", box, uuid.NewString())
	}
	var owner string
	if err == nil {
		err = db.QueryRow("SELECT box FROM echo WHERE singleton=1").Scan(&owner)
	}
	// The row records only that a name was accepted, never the name: the
	// service owns it from then on, and a rename there must not be undone.
	var accepted int
	if err == nil && owner == box {
		err = db.QueryRow("SELECT COUNT(*) FROM box_name WHERE box=?", box).Scan(&accepted)
	}
	if err != nil || owner != box {
		db.Close()
		return fail()
	}
	return &Machine{db: db, lock: lock, paths: paths, now: time.Now, box: box, nameAccepted: accepted > 0}, nil
}

func (m *Machine) Close() error {
	err := m.db.Close()
	_ = syscall.Flock(int(m.lock.Fd()), syscall.LOCK_UN)
	_ = m.lock.Close()
	return err
}

func (m *Machine) snapshot() (syncwire.Snapshot, error) {
	var s syncwire.Snapshot
	var body string
	var revision int64
	err := m.db.QueryRow("SELECT generation,revision,configuration,observed_at FROM echo WHERE singleton=1").Scan(&s.Generation, &revision, &body, &s.ObservedAt)
	if err != nil || body == "" || syncwire.Decode("Configuration", []byte(body), &s.Configuration) != nil {
		return s, ErrState
	}
	s.Schema = 1
	s.Revision = strconv.FormatInt(revision, 10)
	return s, nil
}

func (m *Machine) record(o *observed) (syncwire.Snapshot, error) {
	body, err := json.Marshal(o.configuration)
	if err != nil {
		return syncwire.Snapshot{}, ErrState
	}
	_, err = m.db.Exec("UPDATE echo SET revision=revision+1,configuration=?,observed_at=? WHERE singleton=1 AND configuration!=? AND revision<9223372036854775807", string(body), m.now().UnixMilli(), string(body))
	if err == nil {
		_, err = m.db.Exec("INSERT OR IGNORE INTO revisions SELECT revision,? FROM echo WHERE singleton=1", o.boxDigest)
	}
	if err == nil {
		_, err = m.db.Exec("DELETE FROM revisions WHERE revision < (SELECT revision FROM echo WHERE singleton=1) - ?", keptRevisions)
	}
	if err != nil {
		return syncwire.Snapshot{}, ErrState
	}
	return m.snapshot()
}

// keptRevisions bounds the revision history used to recognise a base that
// differs from the current revision only by delivered account books. A
// proposal lives at most a day; a box rarely makes a thousand revisions in
// one, and an unrecognised base is only judged strictly.
const keptRevisions = 1000

// sameBase reports whether a proposal written against base still describes
// the box's own files: the same revision, or one whose configuration differs
// only in account-managed books. Those are delivered outside the
// one-pending-change slot, and must not turn a pending edit to policy.toml
// or handsets.toml into a conflict.
func (m *Machine) sameBase(base string, current syncwire.Snapshot) bool {
	if base == current.Revision {
		return true
	}
	var then, now string
	err := m.db.QueryRow("SELECT box_digest FROM revisions WHERE revision=?", base).Scan(&then)
	if err == nil {
		err = m.db.QueryRow("SELECT box_digest FROM revisions WHERE revision=?", current.Revision).Scan(&now)
	}
	return err == nil && then == now
}

// Observe reconciles startup and every polling pass, including atomic rename,
// same-mtime edits and changes made while the process was stopped.
func (m *Machine) Observe() (syncwire.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, err := observe(m.paths)
	var rejected *Rejection
	switch {
	case errors.As(err, &rejected):
		m.reject(rejected)
	case err == nil:
		m.accept()
	}
	if err != nil {
		return syncwire.Snapshot{}, err
	}
	return m.record(o)
}

// reject records why the newest content was refused. It logs once per
// distinct content: the watcher looks every second, and a file left broken
// overnight is one line in the journal, not thirty thousand.
func (m *Machine) reject(r *Rejection) {
	if r.key == m.rejectedKey {
		return
	}
	m.rejectedKey = r.key
	m.rejection = &syncwire.ConfigRejection{File: r.File, Message: r.Message, At: m.now().UnixMilli()}
	m.report(r)
}

func (m *Machine) accept() {
	if m.rejection == nil {
		return
	}
	file := m.rejection.File
	m.rejection, m.rejectedKey = nil, ""
	m.report(fmt.Errorf("sync: configuration accepted again after %s was rejected", file))
}

func (m *Machine) report(err error) {
	if m.Warn != nil {
		m.Warn(err)
		return
	}
	fmt.Fprintln(os.Stderr, err)
}

// Rejection is the configRejection field of the status report: present while
// the newest configuration on disk is refused, nil once one is accepted.
func (m *Machine) Rejection() *syncwire.ConfigRejection {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rejection == nil {
		return nil
	}
	r := *m.rejection
	return &r
}

func (m *Machine) Latest() (syncwire.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshot()
}

func (m *Machine) Watch(ctx context.Context, report func(error)) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			_, err := m.Observe()
			if err != nil && report != nil {
				report(err)
			}
		}
	}
}

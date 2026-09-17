-- Run once against AST_LOG_DIR/master.db BEFORE enabling cel_sqlite3_custom.
-- Stop Asterisk before initialising or changing this schema. Use a private
-- directory/file; see docs/events.md. Rerunning preserves identity and history.
-- Do not restore/truncate this spool in place. A new spool needs a new identity.
PRAGMA journal_mode=WAL;
PRAGMA synchronous=FULL;
BEGIN;
CREATE TABLE IF NOT EXISTS doorman_cel_meta (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 version INTEGER NOT NULL CHECK(version=1),
 source_id TEXT NOT NULL
);
INSERT OR IGNORE INTO doorman_cel_meta VALUES(1,1,lower(hex(randomblob(16))));
CREATE TABLE IF NOT EXISTS doorman_cel (
 AcctId INTEGER PRIMARY KEY AUTOINCREMENT,
 eventtype TEXT NOT NULL, eventtime TEXT NOT NULL,
 uniqueid TEXT NOT NULL, linkedid TEXT NOT NULL,
 context TEXT NOT NULL, caller TEXT NOT NULL,
 destination TEXT NOT NULL, app TEXT NOT NULL
);
-- AUTOINCREMENT preserves source cursor order even when retained rows expire.
-- This spool has its own retention: Doorman cannot replay rows it never read.
CREATE TRIGGER IF NOT EXISTS doorman_cel_retention AFTER INSERT ON doorman_cel
BEGIN
 DELETE FROM doorman_cel WHERE AcctId <= NEW.AcctId - 100000;
END;
COMMIT;

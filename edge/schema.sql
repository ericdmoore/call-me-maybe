-- One row per text to any house. The body is here until the box acks it;
-- the carrier's email forwarding is the archive, not this table.
CREATE TABLE IF NOT EXISTS messages (
  house       TEXT NOT NULL,
  id          TEXT NOT NULL,
  received_at TEXT NOT NULL,
  sender      TEXT NOT NULL,
  recipient   TEXT NOT NULL,
  body        TEXT NOT NULL,
  acked_at    TEXT,
  PRIMARY KEY (house, id)
);
CREATE INDEX IF NOT EXISTS messages_pending ON messages (house, acked_at, received_at);

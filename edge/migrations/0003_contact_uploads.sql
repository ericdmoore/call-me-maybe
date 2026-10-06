-- A client retry must not enqueue the same upload twice or silently replace it.
ALTER TABLE messages ADD COLUMN upload_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN source TEXT NOT NULL DEFAULT 'sms';
ALTER TABLE messages ADD COLUMN phonebooks TEXT NOT NULL DEFAULT '[]';
ALTER TABLE messages ADD COLUMN upload_key_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN upload_result TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS messages_sender_time ON messages (house, sender, received_at);

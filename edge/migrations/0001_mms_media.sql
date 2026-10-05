-- Queue only bounded structured contacts. Original files and signed media
-- URLs are not stored or exposed to the house.
ALTER TABLE messages ADD COLUMN media_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN contacts TEXT NOT NULL DEFAULT '[]';
ALTER TABLE messages ADD COLUMN contact_error TEXT NOT NULL DEFAULT '';

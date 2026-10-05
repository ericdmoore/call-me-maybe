-- Keep the carrier's time separate from the Worker's UTC receipt time.
-- Its format/timezone are not specified by the callback documentation.
ALTER TABLE messages ADD COLUMN provider_timestamp TEXT NOT NULL DEFAULT '';

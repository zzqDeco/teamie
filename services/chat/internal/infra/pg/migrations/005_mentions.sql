-- Text ranges and recipient IDs are ordinary Chat-owned message metadata.
ALTER TABLE messages ADD COLUMN mentions jsonb NOT NULL DEFAULT '[]';
ALTER TABLE messages ADD COLUMN original_mentions jsonb NOT NULL DEFAULT '[]';

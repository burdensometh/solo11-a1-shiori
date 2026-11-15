ALTER TABLE bookmark
ADD COLUMN processing_status TEXT NOT NULL DEFAULT '';

ALTER TABLE bookmark
ADD COLUMN processing_failed_step TEXT NOT NULL DEFAULT '';

ALTER TABLE bookmark
ADD COLUMN processing_error TEXT NOT NULL DEFAULT '';

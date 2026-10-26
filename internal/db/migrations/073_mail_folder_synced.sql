-- When go24 last brought a folder fully (GO24DEV-214): a sync cut short by a slow server goes on with the folders
-- waiting longest, not with the same first ones again.
ALTER TABLE mail_folders ADD COLUMN synced_at timestamptz;

-- «Резервное копирование» (GO24DEV-573): a copy goes to an outside place too (S3, SFTP, SMB, WebDAV by rclone-rcd):
-- where it went (for the journal), its folder there (to remove it with the old ones), why it did not go.
ALTER TABLE backup_runs ADD COLUMN target text NOT NULL DEFAULT '';
ALTER TABLE backup_runs ADD COLUMN target_path text NOT NULL DEFAULT '';
ALTER TABLE backup_runs ADD COLUMN upload_error text NOT NULL DEFAULT '';

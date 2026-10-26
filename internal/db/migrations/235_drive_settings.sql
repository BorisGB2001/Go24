-- «Настройки → Диск»: public links, the locking of documents and the search by the text of files may be
-- turned off (kinds 'no_links', 'no_locks', 'no_content': 1 — off, 0 or none — on)
ALTER TABLE drive_limits DROP CONSTRAINT drive_limits_kind_check;
ALTER TABLE drive_limits ADD CONSTRAINT drive_limits_kind_check
	CHECK (kind IN ('user', 'company', 'project', 'versions', 'no_links', 'no_locks', 'no_content'));

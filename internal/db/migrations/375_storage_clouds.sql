-- «Хранилища» (GO24DEV-643): Google Диск, Dropbox and OneDrive are connected by signing in to the account.
ALTER TABLE storages DROP CONSTRAINT storages_type_check;
ALTER TABLE storages ADD CONSTRAINT storages_type_check CHECK (type IN ('webdav', 'yandex', 'mailru', 'smb', 'sftp', 's3', 'ftp',
	'gdrive', 'dropbox', 'onedrive'));

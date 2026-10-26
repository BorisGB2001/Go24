-- «Хранилища» (GO24DEV-578): Яндекс.Диск (WebDAV of webdav.yandex.ru), Облако Mail.ru and a Windows/NAS share by SMB
-- are connected too.
ALTER TABLE storages DROP CONSTRAINT storages_type_check;
ALTER TABLE storages ADD CONSTRAINT storages_type_check CHECK (type IN ('webdav', 'yandex', 'mailru', 'smb', 'sftp', 's3', 'ftp'));

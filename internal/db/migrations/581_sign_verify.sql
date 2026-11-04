-- Подпись: проверка подлинности документа (GO24DEV-746): у каждого документа — код проверки (на листе
-- подписания с QR-кодом), по нему или по самому файлу любой на публичной странице /sg/check видит, что документ
-- подписан в go24, кем и когда. result_hash — SHA-256 подписанного файла (с листом подписания), чтобы узнать и его.
ALTER TABLE sign_docs ADD COLUMN verify_code text NOT NULL DEFAULT upper(substr(md5(gen_random_uuid()::text), 1, 12));
ALTER TABLE sign_docs ADD COLUMN result_hash text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX sign_docs_verify ON sign_docs (verify_code);
CREATE INDEX sign_docs_hash ON sign_docs (hash);
CREATE INDEX sign_docs_result_hash ON sign_docs (result_hash) WHERE result_hash <> '';

-- Поиск по содержимому файлов Диска: the text of a document (Word, Excel, PowerPoint, OpenDocument, PDF,
-- text) is read out in the background (its < and > are no tags to the parser); content_key is the object_key it was
-- read of — a new version is read anew.
ALTER TABLE files
  ADD COLUMN content_key text,
  ADD COLUMN content_text text,
  ADD COLUMN content_tsv tsvector GENERATED ALWAYS AS (to_tsvector('russian', translate(coalesce(content_text, ''), '<>', '  '))) STORED;
CREATE INDEX files_content_tsv_idx ON files USING gin (content_tsv);
CREATE INDEX files_content_due_idx ON files (created_at) WHERE drive IS NOT NULL AND content_key IS DISTINCT FROM object_key;

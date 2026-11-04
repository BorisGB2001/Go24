-- The recorded conversation's copy on the company's drive, in «Записанные звонки» (NULL — none).
ALTER TABLE telephony_calls ADD COLUMN drive_file uuid REFERENCES files (id) ON DELETE SET NULL;

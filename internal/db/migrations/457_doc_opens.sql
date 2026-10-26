-- «Документы»: «Недавние» (GO24DEV-696) — когда сотрудник последний раз открывал документ в редакторе.
CREATE TABLE doc_opens (
    user_id   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    file_id   uuid NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    opened_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, file_id)
);
CREATE INDEX doc_opens_recent ON doc_opens (user_id, opened_at DESC);

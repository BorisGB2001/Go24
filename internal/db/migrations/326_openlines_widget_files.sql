-- «Виджет на сайт»: files in a dialogue (the visitor's and the operator's; a picture is shown), «печатает…»
-- and «прочитано» both ways. A file is kept in the built-in storage; the visitor gets it by its secret, not by the token.
ALTER TABLE ol_messages ADD COLUMN file_key text NOT NULL DEFAULT '',
    ADD COLUMN file_name text NOT NULL DEFAULT '',
    ADD COLUMN file_type text NOT NULL DEFAULT '',
    ADD COLUMN file_size bigint NOT NULL DEFAULT 0,
    ADD COLUMN file_secret text NOT NULL DEFAULT '';
-- a message with a file may have no text
ALTER TABLE ol_messages DROP CONSTRAINT ol_messages_body_check;
ALTER TABLE ol_messages ADD CONSTRAINT ol_messages_body_check CHECK (length(body) <= 5000 AND (length(body) >= 1 OR file_key <> ''));

-- what each side has seen (the last message id) and when it last typed
ALTER TABLE ol_sessions ADD COLUMN op_read bigint NOT NULL DEFAULT 0,
    ADD COLUMN vis_read bigint NOT NULL DEFAULT 0,
    ADD COLUMN op_typing_at timestamptz,
    ADD COLUMN vis_typing_at timestamptz;

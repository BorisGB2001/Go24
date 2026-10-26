-- «Открытые линии»: the quality of service — the visitor rates the answer (👍/👎 and a comment) in the
-- widget, the operator's head gives their own rating; the visitor may get the conversation by e-mail.
ALTER TABLE ol_widgets
    ADD COLUMN vote_on    boolean NOT NULL DEFAULT true, -- ask the visitor to rate the answer
    ADD COLUMN vote_text  text NOT NULL DEFAULT '',       -- the question; '' — the default one
    ADD COLUMN history_on boolean NOT NULL DEFAULT true;  -- «Получить переписку на e-mail»

ALTER TABLE ol_sessions
    ADD COLUMN vote            smallint NOT NULL DEFAULT 0 CHECK (vote IN (-1, 0, 1)),
    ADD COLUMN vote_comment    text NOT NULL DEFAULT '',
    ADD COLUMN voted_at        timestamptz,
    ADD COLUMN head_vote       smallint NOT NULL DEFAULT 0 CHECK (head_vote IN (-1, 0, 1)),
    ADD COLUMN head_comment    text NOT NULL DEFAULT '',
    ADD COLUMN head_by         uuid REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN history_sent_at timestamptz;

DROP TRIGGER ol_sessions_notify ON ol_sessions;
CREATE TRIGGER ol_sessions_notify AFTER UPDATE OF status, operator_id, spam, vote, vote_comment, head_vote, head_comment ON ol_sessions
    FOR EACH ROW EXECUTE FUNCTION go24_openlines_notify();

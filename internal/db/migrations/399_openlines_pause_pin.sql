-- An operator's pause, as «Не принимать новые диалоги»: the queue passes them by. A pinned dialogue
-- («Закрепить за собой») stays with its operator: it is not passed on when they do not answer in time.
CREATE TABLE ol_paused (
    user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    since   timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE ol_sessions ADD COLUMN pinned boolean NOT NULL DEFAULT false;

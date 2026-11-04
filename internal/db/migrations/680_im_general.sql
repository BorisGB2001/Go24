-- «Общий чат» (GO24DEV-854): the employees the company's general chat has already welcomed — the bot says
-- «Новый сотрудник» once for each, not again when they come back to it.
CREATE TABLE im_general_welcomed (
    user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    at      timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE matrix_rooms DROP CONSTRAINT matrix_rooms_kind_check;
ALTER TABLE matrix_rooms ADD CONSTRAINT matrix_rooms_kind_check CHECK (kind IN ('space', 'project', 'task', 'feed', 'notify', 'general'));

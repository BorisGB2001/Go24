-- «Открытые линии»: the queue hands a dialogue to one operator (evenly or strictly in turn) and passes it
-- on when they do not answer in time; working hours with their own message; automatic messages of the line (greeting,
-- «all operators are busy», closing) and closing of dialogues the visitor left.
ALTER TABLE ol_widgets
    ADD COLUMN queue_mode     text NOT NULL DEFAULT 'all' CHECK (queue_mode IN ('all', 'evenly', 'strict')),
    ADD COLUMN answer_wait    int NOT NULL DEFAULT 3 CHECK (answer_wait BETWEEN 1 AND 1440),     -- minutes, then the next operator
    ADD COLUMN hours_on       boolean NOT NULL DEFAULT false,
    ADD COLUMN hours_days     int[] NOT NULL DEFAULT '{1,2,3,4,5}',                               -- 0 Sunday … 6 Saturday
    ADD COLUMN hours_from     text NOT NULL DEFAULT '09:00' CHECK (hours_from ~ '^\d\d:\d\d$'),
    ADD COLUMN hours_to       text NOT NULL DEFAULT '18:00' CHECK (hours_to ~ '^\d\d:\d\d$'),
    ADD COLUMN offline_text   text NOT NULL DEFAULT '' CHECK (length(offline_text) <= 1000),
    ADD COLUMN welcome_text   text NOT NULL DEFAULT '' CHECK (length(welcome_text) <= 1000),
    ADD COLUMN noanswer_text  text NOT NULL DEFAULT '' CHECK (length(noanswer_text) <= 1000),
    ADD COLUMN noanswer_after int NOT NULL DEFAULT 5 CHECK (noanswer_after BETWEEN 1 AND 1440), -- minutes
    ADD COLUMN close_text     text NOT NULL DEFAULT '' CHECK (length(close_text) <= 1000),
    ADD COLUMN autoclose      int NOT NULL DEFAULT 0 CHECK (autoclose BETWEEN 0 AND 720),        -- hours of the visitor's silence; 0 — never
    ADD COLUMN last_operator  uuid;                                                               -- «strictly in turn»: who had the last one

ALTER TABLE ol_sessions
    ADD COLUMN assigned_at   timestamptz,          -- when the operator got it (the queue's clock)
    ADD COLUMN tried         uuid[] NOT NULL DEFAULT '{}', -- operators the dialogue passed by without an answer
    ADD COLUMN answered_at   timestamptz,          -- the first operator answer
    ADD COLUMN waiting_since timestamptz,          -- the first visitor message without an answer
    ADD COLUMN noanswer_sent boolean NOT NULL DEFAULT false;

-- '' — the visitor or an operator; the line's automatic messages (the visitor sees them from the line); 'note' — a
-- service line for operators only (who took, passed, closed the dialogue)
ALTER TABLE ol_messages ADD COLUMN kind text NOT NULL DEFAULT '' CHECK (kind IN ('', 'welcome', 'offline', 'noanswer', 'close', 'note'));

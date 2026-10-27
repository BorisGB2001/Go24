-- «Пульс компании» (GO24DEV-748): which tools of go24 each employee used on a day — one row per
-- employee, tool and day, written on the first page of the tool that day (tool 'mobile' — go24 opened on a phone).
CREATE TABLE user_activity (
    day     date NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tool    text NOT NULL,
    PRIMARY KEY (day, user_id, tool)
);
CREATE INDEX user_activity_user ON user_activity (user_id, day);

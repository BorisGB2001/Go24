-- «Опрос о времени встречи», as Outlook's Scheduling Poll: the organizer offers times of a meeting in a letter, its
-- people (by the link, go24's own and from outside) answer «Да / Возможно / Нет» for each, the meeting is made at the
-- time chosen — by the organizer, or by itself when everyone said «Да» to one (auto).
CREATE TABLE mail_polls (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    mailbox_id uuid REFERENCES mailboxes(id) ON DELETE SET NULL, -- the invitations go from it
    title      text NOT NULL,
    location   text NOT NULL DEFAULT '',
    note       text NOT NULL DEFAULT '',
    length_min int NOT NULL,
    video      boolean NOT NULL DEFAULT false,
    auto       boolean NOT NULL DEFAULT false,
    token      text NOT NULL UNIQUE,
    status     text NOT NULL DEFAULT 'open', -- open, scheduled, cancelled
    chosen_id  uuid,                         -- the option the meeting was made at
    event_id   uuid REFERENCES events(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    closed_at  timestamptz
);
CREATE INDEX mail_polls_owner ON mail_polls (owner_id, created_at DESC);

CREATE TABLE mail_poll_options (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    poll_id   uuid NOT NULL REFERENCES mail_polls(id) ON DELETE CASCADE,
    starts_at timestamptz NOT NULL,
    UNIQUE (poll_id, starts_at)
);

CREATE TABLE mail_poll_voters (
    id       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    poll_id  uuid NOT NULL REFERENCES mail_polls(id) ON DELETE CASCADE,
    email    text NOT NULL,
    name     text NOT NULL DEFAULT '',
    user_id  uuid REFERENCES users(id) ON DELETE SET NULL, -- an employee of go24 with this address
    voted_at timestamptz,
    UNIQUE (poll_id, email)
);

CREATE TABLE mail_poll_votes (
    voter_id  uuid NOT NULL REFERENCES mail_poll_voters(id) ON DELETE CASCADE,
    option_id uuid NOT NULL REFERENCES mail_poll_options(id) ON DELETE CASCADE,
    answer    text NOT NULL, -- yes, maybe, no
    PRIMARY KEY (voter_id, option_id)
);

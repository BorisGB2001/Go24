-- Mail as in Outlook, step 6: the mail with the calendar, tasks and the CRM; shared mailboxes of a department.

-- An invitation a letter carries (iCalendar, RFC 5545/5546): its text/calendar part as it came.
ALTER TABLE mail_messages ADD COLUMN ics text NOT NULL DEFAULT '';

-- The user's answers to invitations by mail: the version answered (SEQUENCE) and the meeting of go24's calendar the
-- invitation is kept as (none when declined).
CREATE TABLE mail_invites (
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    uid        text NOT NULL,
    sequence   int NOT NULL DEFAULT 0,
    status     text NOT NULL CHECK (status IN ('accepted', 'tentative', 'declined', 'cancelled')),
    event_id   uuid REFERENCES events(id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, uid)
);

-- «Под вопросом» for go24's own invitations too.
ALTER TABLE event_attendees DROP CONSTRAINT event_attendees_status_check;
ALTER TABLE event_attendees ADD CONSTRAINT event_attendees_status_check CHECK (status IN ('invited', 'accepted', 'tentative', 'declined'));

-- A go24 meeting sent by mail to people from outside: its UID and version, the mailbox the invitations go from.
ALTER TABLE events
    ADD COLUMN ical_uid   text,
    ADD COLUMN ical_seq   int NOT NULL DEFAULT 0,
    ADD COLUMN mailbox_id uuid REFERENCES mailboxes(id) ON DELETE SET NULL;
CREATE INDEX events_ical_uid_idx ON events (ical_uid) WHERE ical_uid IS NOT NULL;

-- The people from outside invited by mail, and their answers (iTIP REPLY).
CREATE TABLE event_externals (
    event_id uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    email    text NOT NULL CHECK (length(email) BETWEEN 3 AND 300),
    name     text NOT NULL DEFAULT '',
    status   text NOT NULL DEFAULT 'invited' CHECK (status IN ('invited', 'accepted', 'tentative', 'declined')),
    PRIMARY KEY (event_id, email)
);

-- A task made of a letter: the letter (gone — NULL) and what it was, for the task's card.
CREATE TABLE mail_task_links (
    id         bigserial PRIMARY KEY,
    task_id    uuid NOT NULL REFERENCES tasks_all(id) ON DELETE CASCADE,
    message_id uuid REFERENCES mail_messages(id) ON DELETE SET NULL,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    subject    text NOT NULL DEFAULT '',
    from_addr  text NOT NULL DEFAULT '',
    from_name  text NOT NULL DEFAULT '',
    sent_at    timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (task_id, message_id)
);
CREATE INDEX mail_task_links_msg_idx ON mail_task_links (message_id);

-- A letter linked to a CRM card e-mail activity: a copy for the card's timeline (the letter may go
-- from the mailbox; the card keeps it). Later letters of the conversation join the card by themselves.
CREATE TABLE crm_mail (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    entity     text NOT NULL CHECK (entity IN ('company', 'contact', 'deal', 'lead')),
    entity_id  uuid NOT NULL,
    message_id uuid REFERENCES mail_messages(id) ON DELETE SET NULL,
    mailbox_id uuid REFERENCES mailboxes(id) ON DELETE SET NULL,
    thread     text NOT NULL DEFAULT '',
    outgoing   boolean NOT NULL DEFAULT false,
    from_addr  text NOT NULL DEFAULT '',
    from_name  text NOT NULL DEFAULT '',
    to_addr    text NOT NULL DEFAULT '',
    subject    text NOT NULL DEFAULT '',
    body       text NOT NULL DEFAULT '',
    sent_at    timestamptz NOT NULL,
    linked_by  uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (entity, entity_id, message_id)
);
CREATE INDEX crm_mail_msg_idx ON crm_mail (message_id);
CREATE INDEX crm_mail_thread_idx ON crm_mail (mailbox_id, thread) WHERE thread <> '';
ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot', 'document', 'mail'));

-- A shared mailbox of a department: its owner connected it and shares it — with the department (its sub-departments
-- too) at one level, and with people at their own: read, send as the mailbox, manage (the access, the letters).
CREATE TABLE mail_shares (
    mailbox_id    uuid PRIMARY KEY REFERENCES mailboxes(id) ON DELETE CASCADE,
    name          text NOT NULL DEFAULT '' CHECK (length(name) <= 200), -- the sender's name of the letters sent as it
    department_id uuid REFERENCES departments(id) ON DELETE SET NULL,
    dept_level    text NOT NULL DEFAULT 'read' CHECK (dept_level IN ('none', 'read', 'send', 'manage')),
    created_by    uuid NOT NULL REFERENCES users(id),
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE mail_share_people (
    mailbox_id uuid NOT NULL REFERENCES mail_shares(mailbox_id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    level      text NOT NULL CHECK (level IN ('read', 'send', 'manage')),
    PRIMARY KEY (mailbox_id, user_id)
);

-- A conversation of a shared mailbox as a request: who answers it and how far it went (new, in progress, done).
CREATE TABLE mail_tickets (
    mailbox_id  uuid NOT NULL REFERENCES mail_shares(mailbox_id) ON DELETE CASCADE,
    thread      text NOT NULL,  -- the conversation (its root's Message-ID, else the letter's id)
    assignee_id uuid REFERENCES users(id) ON DELETE SET NULL,
    status      text NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'progress', 'done')),
    updated_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (mailbox_id, thread)
);
CREATE INDEX mail_tickets_assignee_idx ON mail_tickets (assignee_id);

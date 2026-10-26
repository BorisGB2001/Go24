-- «Виджет на сайт» и «Открытые линии»: a script on a website opens a chat with go24's operators and a
-- contact form (a CRM lead). A visitor is known by a token kept in their browser (only its hash is stored here).
CREATE TABLE ol_widgets (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    greeting   text NOT NULL DEFAULT '' CHECK (length(greeting) <= 1000),
    color      text NOT NULL DEFAULT '2FC6F6' CHECK (color ~ '^[0-9A-Fa-f]{6}$'),
    origins    text[] NOT NULL DEFAULT '{}', -- sites allowed to show it (https://example.com); empty — any
    operators  uuid[] NOT NULL DEFAULT '{}', -- the queue: who answers (empty — the widget's creator)
    active     boolean NOT NULL DEFAULT true,
    created_by uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE ol_sessions (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    widget_id   uuid NOT NULL REFERENCES ol_widgets(id) ON DELETE CASCADE,
    token_hash  bytea NOT NULL UNIQUE,
    name        text NOT NULL DEFAULT '',
    email       text NOT NULL DEFAULT '',
    phone       text NOT NULL DEFAULT '',
    page        text NOT NULL DEFAULT '',   -- the page the visitor came from
    operator_id uuid REFERENCES users(id) ON DELETE SET NULL,
    lead_id     uuid REFERENCES crm_leads(id) ON DELETE SET NULL,
    status      text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
    unread      int NOT NULL DEFAULT 0,     -- visitor messages no operator has read
    created_at  timestamptz NOT NULL DEFAULT now(),
    last_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ol_sessions_list_idx ON ol_sessions (status, last_at DESC);
CREATE TABLE ol_messages (
    id         bigserial PRIMARY KEY,
    session_id uuid NOT NULL REFERENCES ol_sessions(id) ON DELETE CASCADE,
    author_id  uuid REFERENCES users(id) ON DELETE SET NULL, -- NULL: the visitor
    body       text NOT NULL CHECK (length(body) BETWEEN 1 AND 5000),
    at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ol_messages_idx ON ol_messages (session_id, id);

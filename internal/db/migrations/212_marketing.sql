-- «Маркетинг» (CRM → Маркетинг): segments of addressees, e-mail campaigns sent from a go24 mailbox in
-- the background, the stop list of those who unsubscribed.

-- A segment: the CRM cards of a filter (contacts, companies, leads) or a list of addresses.
CREATE TABLE mkt_segments (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    entity     text NOT NULL CHECK (entity IN ('contact', 'company', 'lead', 'list')),
    filter     jsonb NOT NULL DEFAULT '{}',
    emails     text NOT NULL DEFAULT '', -- entity 'list': an address a line, «Имя <адрес>» or «адрес;Имя»
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- A campaign: a letter to the addressees of its segments but those of the excluded ones and of the stop list.
CREATE TABLE mkt_campaigns (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title       text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    mailbox_id  uuid REFERENCES mailboxes(id) ON DELETE SET NULL,
    subject     text NOT NULL DEFAULT '',
    body        text NOT NULL DEFAULT '',
    status      text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'sending', 'paused', 'done', 'stopped')),
    send_at     timestamptz, -- sent from then on (a planned campaign)
    started_at  timestamptz,
    finished_at timestamptz,
    created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mkt_campaigns_status ON mkt_campaigns (status, send_at);

CREATE TABLE mkt_campaign_segments (
    campaign_id uuid NOT NULL REFERENCES mkt_campaigns(id) ON DELETE CASCADE,
    segment_id  uuid NOT NULL REFERENCES mkt_segments(id) ON DELETE CASCADE,
    exclude     bool NOT NULL DEFAULT false,
    PRIMARY KEY (campaign_id, segment_id)
);

-- The addressees of a started campaign, fixed when it starts; token — of the links in their letter.
CREATE TABLE mkt_recipients (
    id              bigserial PRIMARY KEY,
    campaign_id     uuid NOT NULL REFERENCES mkt_campaigns(id) ON DELETE CASCADE,
    email           text NOT NULL,
    name            text NOT NULL DEFAULT '',
    entity          text NOT NULL DEFAULT '',
    entity_id       uuid,
    token           text NOT NULL UNIQUE,
    status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'error', 'skipped')),
    error           text NOT NULL DEFAULT '',
    sent_at         timestamptz,
    opened_at       timestamptz,
    clicked_at      timestamptz,
    unsubscribed_at timestamptz,
    UNIQUE (campaign_id, email)
);
CREATE INDEX mkt_recipients_pending ON mkt_recipients (campaign_id, id) WHERE status = 'pending';

-- «Стоп-лист»: the addresses no campaign goes to (lower case).
CREATE TABLE mkt_stoplist (
    email       text PRIMARY KEY,
    campaign_id uuid REFERENCES mkt_campaigns(id) ON DELETE SET NULL, -- the campaign they unsubscribed from
    added_by    uuid REFERENCES users(id) ON DELETE SET NULL,        -- or the employee who added them
    created_at  timestamptz NOT NULL DEFAULT now()
);

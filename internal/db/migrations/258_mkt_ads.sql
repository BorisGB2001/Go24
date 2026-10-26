-- «Рекламные источники» and «Сквозная аналитика»: the UTM tags a lead or a deal came with, the
-- advertising channels of the company with the utm_source values and the CRM «Источник» that are theirs, and what
-- was spent on each channel a day — the report counts the expenses against the leads, deals and revenue.

-- UTM tags of a lead or a deal (a deal made of a lead without its own takes the lead's).
CREATE TABLE mkt_utm (
    lead_id  uuid UNIQUE REFERENCES crm_leads(id) ON DELETE CASCADE,
    deal_id  uuid UNIQUE REFERENCES crm_deals(id) ON DELETE CASCADE,
    source   text NOT NULL DEFAULT '',
    medium   text NOT NULL DEFAULT '',
    campaign text NOT NULL DEFAULT '',
    content  text NOT NULL DEFAULT '',
    term     text NOT NULL DEFAULT '',
    CHECK (num_nonnulls(lead_id, deal_id) = 1)
);

-- An advertising channel: Яндекс Директ, VK Реклама… A card is the channel's when its utm_source is one of utm
-- (in any case) or, without UTM tags, its «Источник» is crm_source.
CREATE TABLE mkt_ad_sources (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    utm        text[] NOT NULL DEFAULT '{}',
    crm_source uuid REFERENCES crm_dict(id) ON DELETE SET NULL,
    color      text NOT NULL DEFAULT '',
    sort       int NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- What was spent on a channel a day; campaign — the utm_campaign it was spent on ("" — the channel as a whole).
CREATE TABLE mkt_ad_expenses (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source_id  uuid NOT NULL REFERENCES mkt_ad_sources(id) ON DELETE CASCADE,
    day        date NOT NULL,
    amount     numeric(18, 2) NOT NULL CHECK (amount >= 0),
    currency   text NOT NULL,
    campaign   text NOT NULL DEFAULT '',
    comment    text NOT NULL DEFAULT '',
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mkt_ad_expenses_day ON mkt_ad_expenses (day, source_id);

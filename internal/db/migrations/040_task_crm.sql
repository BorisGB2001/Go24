-- «Привязка к CRM» of a portal task: links to CRM items (deals, contacts, companies, leads, smart processes).
-- go24 has no CRM yet, so an item is the URL of the portal's card; its type and number come from the URL.
CREATE TABLE task_crm_links (
    task_id    uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    url        text NOT NULL CHECK (length(url) BETWEEN 1 AND 1000),
    kind       text NOT NULL CHECK (kind IN ('deal', 'contact', 'company', 'lead', 'quote', 'invoice', 'item', 'other')),
    entity_id  bigint, -- the number in the CRM (NULL for an unknown link)
    created_by uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, url)
);

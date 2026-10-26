-- «Связи» of smart processes: the kinds of cards an element of a smart process may be bound to
-- (parent '' — deals, else a smart process), and the parents of each card. The child cards show in their parents' cards.
CREATE TABLE crm_type_relations (
    type_id uuid NOT NULL REFERENCES crm_types(id) ON DELETE CASCADE,
    parent  text NOT NULL,
    sort    int NOT NULL DEFAULT 0,
    PRIMARY KEY (type_id, parent)
);

CREATE TABLE crm_deal_parents (
    deal_id   uuid NOT NULL REFERENCES crm_deals(id) ON DELETE CASCADE,
    parent_id uuid NOT NULL REFERENCES crm_deals(id) ON DELETE CASCADE,
    PRIMARY KEY (deal_id, parent_id),
    CHECK (deal_id <> parent_id)
);
CREATE INDEX crm_deal_parents_parent_idx ON crm_deal_parents (parent_id);

-- the timeline of the child tells its binding and unbinding
ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check
    CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot', 'document', 'mail', 'activity', 'invoice', 'merged', 'recurred',
        'recurred_from', 'changed', 'tunnel_to', 'tunnel_from', 'pipeline', 'openline', 'call', 'mailing', 'restored', 'booking',
        'ship_auto', 'ship_draft', 'repeat', 'bound', 'unbound'));

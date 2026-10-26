-- «Предложения» and «Счета» of the portal CRM: made from a deal with its products, numbered per kind, with a
-- status; printed for the client. Their product rows are those of crm_product_rows (entity 'quote' / 'invoice').
CREATE TABLE crm_invoices (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind           text NOT NULL CHECK (kind IN ('quote', 'invoice')),
    number         int NOT NULL,
    deal_id        uuid NOT NULL REFERENCES crm_deals(id) ON DELETE CASCADE,
    title          text NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
    status         text NOT NULL,
    issue_date     date NOT NULL DEFAULT CURRENT_DATE,
    due_date       date,
    amount         numeric(18, 2) NOT NULL DEFAULT 0,
    currency       text NOT NULL DEFAULT 'RUB',
    comment        text NOT NULL DEFAULT '' CHECK (length(comment) <= 10000),
    responsible_id uuid NOT NULL REFERENCES users(id),
    created_by     uuid NOT NULL REFERENCES users(id),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (kind, number),
    CHECK ((kind = 'quote' AND status IN ('draft', 'sent', 'accepted', 'declined'))
        OR (kind = 'invoice' AND status IN ('new', 'sent', 'paid', 'cancelled')))
);
CREATE INDEX crm_invoices_deal_idx ON crm_invoices (deal_id);

ALTER TABLE crm_product_rows DROP CONSTRAINT crm_product_rows_entity_check;
ALTER TABLE crm_product_rows ADD CONSTRAINT crm_product_rows_entity_check CHECK (entity IN ('deal', 'lead', 'quote', 'invoice'));
-- a deleted quote or invoice takes its rows along
CREATE FUNCTION crm_product_rows_gone_kind() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM crm_product_rows WHERE entity = OLD.kind AND entity_id = OLD.id;
    RETURN NULL;
END $$;
CREATE TRIGGER crm_invoices_product_rows AFTER DELETE ON crm_invoices FOR EACH ROW EXECUTE FUNCTION crm_product_rows_gone_kind();

ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check
    CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot', 'document', 'mail', 'activity', 'invoice'));

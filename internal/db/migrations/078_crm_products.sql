-- «Товары» of the portal CRM: a catalog of products and services, and the product rows of a deal or a lead.
-- A card with rows has its amount counted from them.
CREATE TABLE crm_products (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 300),
    sku          text NOT NULL DEFAULT '' CHECK (length(sku) <= 100),     -- артикул
    price        numeric(18, 2) NOT NULL DEFAULT 0 CHECK (price >= 0),
    currency     text NOT NULL DEFAULT 'RUB',
    unit         text NOT NULL DEFAULT '' CHECK (length(unit) <= 20),     -- empty: the locale's «шт.»
    vat_rate     numeric(5, 2) CHECK (vat_rate BETWEEN 0 AND 100),        -- NULL: no VAT
    vat_included boolean NOT NULL DEFAULT true,
    description  text NOT NULL DEFAULT '' CHECK (length(description) <= 10000),
    active       boolean NOT NULL DEFAULT true,
    created_by   uuid NOT NULL REFERENCES users(id),
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE crm_product_rows (
    id           bigserial PRIMARY KEY,
    entity       text NOT NULL CHECK (entity IN ('deal', 'lead')),
    entity_id    uuid NOT NULL,
    product_id   uuid REFERENCES crm_products(id) ON DELETE SET NULL,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 300),
    price        numeric(18, 2) NOT NULL CHECK (price >= 0),
    quantity     numeric(18, 3) NOT NULL CHECK (quantity > 0),
    unit         text NOT NULL DEFAULT '',
    discount     numeric(5, 2) NOT NULL DEFAULT 0 CHECK (discount BETWEEN 0 AND 100), -- percent
    vat_rate     numeric(5, 2) CHECK (vat_rate BETWEEN 0 AND 100),
    vat_included boolean NOT NULL DEFAULT true,
    sort         int NOT NULL DEFAULT 0
);
CREATE INDEX crm_product_rows_idx ON crm_product_rows (entity, entity_id, sort);

-- a deleted card takes its rows along
CREATE FUNCTION crm_product_rows_gone() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM crm_product_rows WHERE entity = TG_ARGV[0] AND entity_id = OLD.id;
    RETURN NULL;
END $$;
CREATE TRIGGER crm_deals_product_rows AFTER DELETE ON crm_deals FOR EACH ROW EXECUTE FUNCTION crm_product_rows_gone('deal');
CREATE TRIGGER crm_leads_product_rows AFTER DELETE ON crm_leads FOR EACH ROW EXECUTE FUNCTION crm_product_rows_gone('lead');

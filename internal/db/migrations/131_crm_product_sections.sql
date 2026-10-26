-- «Каталог товаров»: sections of the catalog (a tree), a product's section, picture and purchase price.
CREATE TABLE crm_product_sections (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    parent_id  uuid REFERENCES crm_product_sections(id) ON DELETE CASCADE, -- NULL: a top section
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    sort       integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX crm_product_sections_parent ON crm_product_sections (parent_id);

ALTER TABLE crm_products
    ADD COLUMN section_id     uuid REFERENCES crm_product_sections(id) ON DELETE SET NULL, -- NULL: the catalog's root
    ADD COLUMN purchase_price numeric(18, 2) CHECK (purchase_price >= 0),                  -- NULL: not given
    ADD COLUMN photo_key      text NOT NULL DEFAULT '',                                    -- the picture in the storage
    ADD COLUMN photo_type     text NOT NULL DEFAULT '';
CREATE INDEX crm_products_section ON crm_products (section_id);

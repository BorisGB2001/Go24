-- The catalog's dictionaries: «Единицы измерения» (by ОКЕИ: a built-in unit has its code and takes its
-- name and symbol from the locale until they are changed), «Свойства товаров» (a string, a number, a list or yes/no;
-- the values live with the product) and the products' barcodes (several to a product, one product to a barcode).
CREATE TABLE crm_units (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code       text NOT NULL DEFAULT '' CHECK (length(code) <= 10),   -- ОКЕИ
    name       text NOT NULL DEFAULT '' CHECK (length(name) <= 100),  -- empty: the locale's name of the code
    symbol     text NOT NULL DEFAULT '' CHECK (length(symbol) <= 20), -- empty: the locale's symbol of the code
    is_default boolean NOT NULL DEFAULT false,
    sort       integer NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX crm_units_default ON crm_units (is_default) WHERE is_default;
INSERT INTO crm_units (code, is_default, sort) VALUES ('796', true, 10), ('166', false, 20), ('163', false, 30), ('006', false, 40),
    ('055', false, 50), ('112', false, 60), ('113', false, 70), ('778', false, 80), ('839', false, 90), ('356', false, 100);

CREATE TABLE crm_product_props (
    id      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name    text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    kind    text NOT NULL CHECK (kind IN ('string', 'number', 'list', 'bool')),
    options text[] NOT NULL DEFAULT '{}', -- the values of a list
    in_list boolean NOT NULL DEFAULT false, -- a column of the catalog's list
    sort    integer NOT NULL DEFAULT 0
);

ALTER TABLE crm_products ADD COLUMN props jsonb NOT NULL DEFAULT '{}'; -- a property's id → its value

CREATE TABLE crm_product_barcodes (
    barcode    text PRIMARY KEY CHECK (length(barcode) BETWEEN 1 AND 64),
    product_id uuid NOT NULL REFERENCES crm_products(id) ON DELETE CASCADE
);
CREATE INDEX crm_product_barcodes_product ON crm_product_barcodes (product_id);

-- «Вариации товара» (торговые предложения): a variation is a product of its own (its name, SKU, price,
-- barcodes, photo and stock) under the product it varies; the deals and the warehouse documents take the variation.
-- The properties marked «для вариаций» tell the variations apart (colour, size).
ALTER TABLE crm_products ADD COLUMN parent_id uuid REFERENCES crm_products(id) ON DELETE CASCADE;
CREATE INDEX crm_products_parent ON crm_products (parent_id) WHERE parent_id IS NOT NULL;
ALTER TABLE crm_product_props ADD COLUMN variant boolean NOT NULL DEFAULT false;

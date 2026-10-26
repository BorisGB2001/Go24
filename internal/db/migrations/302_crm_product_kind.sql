-- «Тип товара» of the portal catalog: a product or a service. A service is sold in the deals, the quotes and the
-- invoices as a product is, but isn't kept on the warehouses: no stock, reserve, warehouse documents or variations.
ALTER TABLE crm_products ADD COLUMN kind text NOT NULL DEFAULT 'product' CHECK (kind IN ('product', 'service'));

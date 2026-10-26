-- The reserve and «Реализация» of the «Складской учёт»: a deal's product row is reserved on a warehouse
-- (how many, till when — no date: while the deal is open); the available stock is what is there less the reserves
-- of open deals. «Реализация» is a stock document of a deal: conducted, it takes its products from the warehouses
-- and what it shipped is no longer reserved.
ALTER TABLE crm_product_rows
    ADD COLUMN warehouse_id  uuid REFERENCES crm_warehouses(id) ON DELETE SET NULL,
    ADD COLUMN reserve       numeric(18, 3) NOT NULL DEFAULT 0 CHECK (reserve >= 0),
    ADD COLUMN reserve_until date;
CREATE INDEX crm_product_rows_reserve ON crm_product_rows (product_id, warehouse_id) WHERE reserve > 0;

ALTER TABLE crm_stock_docs DROP CONSTRAINT crm_stock_docs_kind_check;
ALTER TABLE crm_stock_docs ADD CONSTRAINT crm_stock_docs_kind_check CHECK (kind IN ('arrival', 'adjustment', 'moving', 'deduct', 'shipment'));
ALTER TABLE crm_stock_docs ADD COLUMN deal_id uuid REFERENCES crm_deals(id) ON DELETE SET NULL; -- a shipment's deal
CREATE INDEX crm_stock_docs_deal ON crm_stock_docs (deal_id) WHERE deal_id IS NOT NULL;

-- «Складской учёт»: warehouses, the stock of catalog products on each, and the stock documents that move
-- it — «Приход» (from a supplier), «Оприходование», «Перемещение», «Списание». A document changes the stock when it
-- is conducted («Проведён») and gives it back when the conducting is cancelled («Отменён»).
CREATE TABLE crm_warehouses (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name           text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    address        text NOT NULL DEFAULT '' CHECK (length(address) <= 500),
    responsible_id uuid REFERENCES users(id) ON DELETE SET NULL, -- the storekeeper: keeps the documents with administrators
    is_default     boolean NOT NULL DEFAULT false,
    active         boolean NOT NULL DEFAULT true,
    sort           integer NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX crm_warehouses_default ON crm_warehouses (is_default) WHERE is_default;

-- the stock: what is on a warehouse now (only conducted documents change it)
CREATE TABLE crm_stock (
    product_id   uuid NOT NULL REFERENCES crm_products(id) ON DELETE CASCADE,
    warehouse_id uuid NOT NULL REFERENCES crm_warehouses(id) ON DELETE CASCADE,
    quantity     numeric(18, 3) NOT NULL DEFAULT 0 CHECK (quantity >= 0),
    PRIMARY KEY (product_id, warehouse_id)
);
CREATE INDEX crm_stock_warehouse ON crm_stock (warehouse_id);

CREATE TABLE crm_stock_docs (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind            text NOT NULL CHECK (kind IN ('arrival', 'adjustment', 'moving', 'deduct')),
    number          integer NOT NULL UNIQUE,
    title           text NOT NULL DEFAULT '' CHECK (length(title) <= 300),
    status          text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'conducted', 'cancelled')),
    doc_date        date NOT NULL DEFAULT CURRENT_DATE,
    supplier_id     uuid REFERENCES crm_companies(id) ON DELETE SET NULL, -- «Поставщик» of an arrival
    incoming_number text NOT NULL DEFAULT '' CHECK (length(incoming_number) <= 100),
    incoming_date   date,
    reason          text NOT NULL DEFAULT '' CHECK (length(reason) <= 500), -- «Причина списания»
    comment         text NOT NULL DEFAULT '' CHECK (length(comment) <= 10000),
    currency        text NOT NULL DEFAULT 'RUB',
    amount          numeric(18, 2) NOT NULL DEFAULT 0, -- the rows' sum at their prices
    responsible_id  uuid NOT NULL REFERENCES users(id),
    created_by      uuid NOT NULL REFERENCES users(id),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    conducted_by    uuid REFERENCES users(id),
    conducted_at    timestamptz
);
CREATE INDEX crm_stock_docs_date ON crm_stock_docs (doc_date DESC, number DESC);

-- a row: a catalog product, how many, at what price, from which warehouse and to which (as the kind needs)
CREATE TABLE crm_stock_doc_rows (
    id             bigserial PRIMARY KEY,
    doc_id         uuid NOT NULL REFERENCES crm_stock_docs(id) ON DELETE CASCADE,
    product_id     uuid REFERENCES crm_products(id) ON DELETE SET NULL, -- a product gone: the row keeps its name
    name           text NOT NULL CHECK (length(name) BETWEEN 1 AND 300),
    quantity       numeric(18, 3) NOT NULL CHECK (quantity > 0),
    price          numeric(18, 2) NOT NULL DEFAULT 0 CHECK (price >= 0),
    from_warehouse uuid REFERENCES crm_warehouses(id),
    to_warehouse   uuid REFERENCES crm_warehouses(id),
    sort           integer NOT NULL DEFAULT 0
);
CREATE INDEX crm_stock_doc_rows_doc ON crm_stock_doc_rows (doc_id, sort);
CREATE INDEX crm_stock_doc_rows_product ON crm_stock_doc_rows (product_id);

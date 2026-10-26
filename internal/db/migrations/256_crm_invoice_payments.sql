-- Payments of an invoice, as «Оплаты»: one invoice is paid in parts; paid in full it is «Оплачен».
CREATE TABLE crm_invoice_payments (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id uuid NOT NULL REFERENCES crm_invoices(id) ON DELETE CASCADE,
    paid_on    date NOT NULL DEFAULT CURRENT_DATE,
    amount     numeric(18, 2) NOT NULL CHECK (amount > 0),
    comment    text NOT NULL DEFAULT '' CHECK (length(comment) <= 1000),
    created_by uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX crm_invoice_payments_invoice ON crm_invoice_payments (invoice_id);

-- an invoice made from a quote («Создать на основании»), and the client's link to the document
ALTER TABLE crm_invoices ADD COLUMN source_id uuid REFERENCES crm_invoices(id) ON DELETE SET NULL;
ALTER TABLE crm_invoices ADD COLUMN public_token text UNIQUE;

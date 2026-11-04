-- «Платёжные системы»: the company's online payment services (ЮKassa, Т-Банк) through which a client
-- pays an invoice by its link, and the payments started there; a confirmed one is a payment of the invoice.
CREATE TABLE pay_systems (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind       text NOT NULL CHECK (kind IN ('yookassa', 'tbank')),
    name       text NOT NULL,
    login      text NOT NULL DEFAULT '', -- ЮKassa shopId, Т-Банк TerminalKey
    secret     bytea,                    -- the secret key or the terminal's password, sealed
    active     boolean NOT NULL DEFAULT true,
    sort       int NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- A payment started by the client: pending until the service says it is paid (succeeded) or not (canceled).
CREATE TABLE pay_payments (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    system_id   uuid NOT NULL REFERENCES pay_systems(id) ON DELETE CASCADE,
    invoice_id  uuid NOT NULL REFERENCES crm_invoices(id) ON DELETE CASCADE,
    external_id text NOT NULL DEFAULT '',
    amount      numeric(18, 2) NOT NULL CHECK (amount > 0),
    currency    text NOT NULL,
    status      text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'succeeded', 'canceled')),
    url         text NOT NULL DEFAULT '', -- the service's page where the client pays
    error       text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    paid_at     timestamptz
);
CREATE INDEX pay_payments_invoice_idx ON pay_payments (invoice_id, created_at);
CREATE INDEX pay_payments_external_idx ON pay_payments (system_id, external_id);

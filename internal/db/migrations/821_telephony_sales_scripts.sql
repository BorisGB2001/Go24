-- «Скрипты продаж»: a conversation's scenario of blocks — what the employee says and the client's answers,
-- each leading to the next block; the softphone's call card walks it during a call (GO24DEV-917).
CREATE TABLE telephony_sales_scripts (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL,
    body       text NOT NULL,                -- the blocks in the script's markup (telephony.ParseSalesScript)
    calls      text NOT NULL DEFAULT 'all',  -- all | in | out
    active     boolean NOT NULL DEFAULT true,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

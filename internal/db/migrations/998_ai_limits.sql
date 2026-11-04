-- The usage of AI (GO24DEV-482): a connection has its prices (per million tokens of the input and of the output, in
-- US dollars) and, the company's one, a monthly limit of tokens of an employee (0 — none); ai_limits is
-- an employee's own limit over the connection's. A call of a model notes its cost by the prices of that moment.
ALTER TABLE ai_connections
    ADD COLUMN price_in      numeric(12, 4) NOT NULL DEFAULT 0,
    ADD COLUMN price_out     numeric(12, 4) NOT NULL DEFAULT 0,
    ADD COLUMN monthly_limit bigint NOT NULL DEFAULT 0;
ALTER TABLE ai_usage ADD COLUMN cost numeric(14, 6) NOT NULL DEFAULT 0;
CREATE INDEX ai_usage_created ON ai_usage (created_at);
CREATE TABLE ai_limits (
    user_id        uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    monthly_tokens bigint NOT NULL -- 0 — no limit
);

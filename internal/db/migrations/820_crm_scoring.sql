-- «Скоринг сделок» CRM: a pipeline of deals learns from its closed deals (won and lost) how likely an open
-- one is to be won. The model is kept here; the probability of a deal is counted from it when the deal is shown.
CREATE TABLE crm_scoring (
    pipeline_id uuid PRIMARY KEY REFERENCES crm_pipelines(id) ON DELETE CASCADE,
    enabled     boolean NOT NULL DEFAULT false,
    model       jsonb, -- NULL: not trained yet or too few closed deals
    samples     int NOT NULL DEFAULT 0, -- closed deals it learned from
    won         int NOT NULL DEFAULT 0,
    accuracy    float8 NOT NULL DEFAULT 0, -- share of the closed deals guessed right (each without itself)
    trained_at  timestamptz,
    updated_by  uuid REFERENCES users(id) ON DELETE SET NULL
);

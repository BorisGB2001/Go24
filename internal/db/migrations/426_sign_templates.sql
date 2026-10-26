-- «Подпись»: шаблоны отправки (GO24DEV-673) — документ, подписанты компании и напоминания сохраняются
-- один раз; по шаблону остаётся указать клиентов.
CREATE TABLE sign_templates (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title       text NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
    file_id     uuid NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    signers     uuid[] NOT NULL DEFAULT '{}',          -- the company's signers, in turn
    remind_days int NOT NULL DEFAULT 1 CHECK (remind_days BETWEEN 0 AND 30),
    created_by  uuid NOT NULL REFERENCES users(id),
    created_at  timestamptz NOT NULL DEFAULT now()
);

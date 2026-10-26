-- «Рабочие отчёты» (timeman): an employee sends their manager a report for a day, week or month — the
-- worked time, tasks and meetings of the period (kept as they were when sent) and a text; the manager marks it
-- (good / bad / without a mark) and both discuss it. When reports are due is a setting (company_settings "work_reports").
CREATE TABLE work_reports (
    id         bigserial PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    date_from  date NOT NULL,
    date_to    date NOT NULL CHECK (date_to >= date_from),
    body       text NOT NULL DEFAULT '' CHECK (length(body) <= 10000),
    worked     int NOT NULL DEFAULT 0,             -- seconds worked in the period
    items      jsonb NOT NULL DEFAULT '{}',        -- {"tasks": [...], "events": [...]} of the period
    sent_at    timestamptz NOT NULL DEFAULT now(),
    mark       text NOT NULL DEFAULT '' CHECK (mark IN ('', 'good', 'bad', 'none')), -- '' — not checked yet
    marked_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    marked_at  timestamptz
);
CREATE INDEX work_reports_user_idx ON work_reports (user_id, date_to DESC);

CREATE TABLE work_report_comments (
    id         bigserial PRIMARY KEY,
    report_id  bigint NOT NULL REFERENCES work_reports(id) ON DELETE CASCADE,
    author_id  uuid REFERENCES users(id) ON DELETE SET NULL,
    body       text NOT NULL CHECK (length(body) BETWEEN 1 AND 5000),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX work_report_comments_idx ON work_report_comments (report_id, id);

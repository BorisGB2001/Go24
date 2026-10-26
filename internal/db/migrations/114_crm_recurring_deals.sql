-- «Регулярная сделка»: a deal repeated on schedule — every N days, weeks or months from start_date, until
-- until_date or limit_count copies. next_date is the day of the next copy; the worker makes it then.
CREATE TABLE crm_deal_recurrence (
    deal_id       uuid PRIMARY KEY REFERENCES crm_deals(id) ON DELETE CASCADE,
    unit          text NOT NULL CHECK (unit IN ('day', 'week', 'month')),
    every         int NOT NULL CHECK (every BETWEEN 1 AND 365),
    start_date    date NOT NULL,
    next_date     date NOT NULL,
    until_date    date,
    limit_count   int CHECK (limit_count > 0),
    created_count int NOT NULL DEFAULT 0,
    created_by    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX crm_deal_recurrence_next ON crm_deal_recurrence (next_date);

-- the timelines of the deal and its copy point to each other
ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check
    CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot', 'document', 'mail', 'activity', 'invoice', 'merged',
        'recurred', 'recurred_from'));

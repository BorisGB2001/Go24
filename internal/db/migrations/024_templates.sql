-- «Шаблоны задач» and regular tasks (the portal tasks.template, REPLICATE): a template keeps the fields of a task,
-- with the deadline as an offset from the moment a task is made; a regular template makes tasks on a schedule.
CREATE TABLE task_templates (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    creator_id            uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE, -- tasks are set by this person
    project_id            uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title                 text NOT NULL CHECK (length(title) BETWEEN 1 AND 500),
    description           text NOT NULL DEFAULT '',
    responsible_id        uuid NOT NULL REFERENCES users(id),
    priority              smallint NOT NULL DEFAULT 1 CHECK (priority BETWEEN 0 AND 2),
    deadline_after        integer CHECK (deadline_after > 0),   -- minutes after creation (NULL = no deadline)
    requires_control      boolean NOT NULL DEFAULT false,
    allow_change_deadline boolean NOT NULL DEFAULT false,
    match_work_time       boolean NOT NULL DEFAULT false,
    accomplices           uuid[] NOT NULL DEFAULT '{}',
    auditors              uuid[] NOT NULL DEFAULT '{}',
    checklist             text[] NOT NULL DEFAULT '{}',
    -- repetition
    replicate             boolean NOT NULL DEFAULT false,
    period                text NOT NULL DEFAULT 'weekly' CHECK (period IN ('daily', 'weekly', 'monthly', 'yearly')),
    every                 integer NOT NULL DEFAULT 1 CHECK (every BETWEEN 1 AND 365),
    workdays_only         boolean NOT NULL DEFAULT false,      -- daily: skip weekends and holidays
    week_days             smallint[] NOT NULL DEFAULT '{1}',   -- weekly: 1 = Monday … 7 = Sunday
    month_day             smallint NOT NULL DEFAULT 1 CHECK (month_day BETWEEN 1 AND 31), -- monthly, yearly (31 = last day)
    year_month            smallint NOT NULL DEFAULT 1 CHECK (year_month BETWEEN 1 AND 12),
    at_time               text NOT NULL DEFAULT '09:00',       -- HH:MM, server time zone
    starts_on             date NOT NULL DEFAULT current_date,
    ends_on               date,                                -- NULL = no end date
    times                 integer CHECK (times > 0),           -- NULL = no limit
    created_count         integer NOT NULL DEFAULT 0,
    next_run              timestamptz,                         -- NULL = nothing planned
    last_run              timestamptz,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX task_templates_creator_idx ON task_templates (creator_id);
CREATE INDEX task_templates_due_idx ON task_templates (next_run) WHERE replicate AND next_run IS NOT NULL;

ALTER TABLE tasks ADD COLUMN template_id uuid REFERENCES task_templates(id) ON DELETE SET NULL;

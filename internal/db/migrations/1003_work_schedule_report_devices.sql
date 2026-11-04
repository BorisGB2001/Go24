-- «Отчёт о рабочем дне» and «Устройства для отметки» of a portal work schedule: the report on finishing a day is
-- optional, required or not asked; the day is marked only from the allowed devices (the browser, a time terminal,
-- apps by REST).
ALTER TABLE work_schedules
    ADD COLUMN report_req text NOT NULL DEFAULT 'optional' CHECK (report_req IN ('optional', 'required', 'none')),
    ADD COLUMN devices text[] NOT NULL DEFAULT '{browser,terminal,app}';

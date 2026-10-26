-- «Показывать в разделе»: a dashboard of «BI-аналитика» pinned to the sections — CRM, tasks, the company
-- (the working time) — opens there, on their «BI-аналитика» tab, for those who see it. The ready «Продажи» and
-- «Лиды и источники» are in the CRM, «Задачи и эффективность» in the tasks, «Рабочее время» in the company.
ALTER TABLE bi_dashboards ADD COLUMN sections text[] NOT NULL DEFAULT '{}';
UPDATE bi_dashboards SET sections = CASE code WHEN 'sales' THEN '{crm}' WHEN 'leads' THEN '{crm}'
    WHEN 'tasks' THEN '{tasks}' WHEN 'timeman' THEN '{company}' END::text[]
    WHERE code IN ('sales', 'leads', 'tasks', 'timeman');

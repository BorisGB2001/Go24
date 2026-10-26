-- «Фильтры дашборда», as the native filters of the BI Конструктор: the fields of its widgets a dashboard is
-- narrowed by besides the period (the responsible, the pipeline, the source…), picked on the dashboard itself.
ALTER TABLE bi_dashboards ADD COLUMN filters text[] NOT NULL DEFAULT '{}';

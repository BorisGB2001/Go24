-- More kinds of widgets, as the charts of the BI Конструктор: stacked columns (stack), areas, a funnel, a pivot
-- table, a text; compare — a number shows its change against the period before, body — the text of a text widget.
ALTER TABLE bi_widgets DROP CONSTRAINT bi_widgets_kind_check;
ALTER TABLE bi_widgets ADD CONSTRAINT bi_widgets_kind_check
    CHECK (kind IN ('kpi', 'bar', 'stack', 'line', 'area', 'pie', 'funnel', 'table', 'pivot', 'text'));
ALTER TABLE bi_widgets ADD COLUMN compare boolean NOT NULL DEFAULT false,
    ADD COLUMN body text NOT NULL DEFAULT '' CHECK (length(body) <= 10000);

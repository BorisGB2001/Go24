-- «BI-аналитика», as the «BI Конструктор»: dashboards of widgets over the data sets. A dashboard has a period
-- that applies to all its widgets (period: all, today, week, month, quarter, year, last_7, last_30, prev_month,
-- prev_quarter, prev_year, range — from date_from to date_to); a viewer may pick another one on the page.
CREATE TABLE bi_dashboards (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title       text NOT NULL,
    description text NOT NULL DEFAULT '',
    period      text NOT NULL DEFAULT 'month',
    date_from   date,
    date_to     date,
    created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- A widget: a number (kpi), columns (bar), a line, a pie or a table, of the query (bi.Query as JSON without the
-- period) placed at pos in the dashboard's grid, width columns of 12 wide; no_period — the dashboard's period does
-- not apply to it.
CREATE TABLE bi_widgets (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    dashboard_id uuid NOT NULL REFERENCES bi_dashboards(id) ON DELETE CASCADE,
    pos          int NOT NULL DEFAULT 0,
    kind         text NOT NULL CHECK (kind IN ('kpi', 'bar', 'line', 'pie', 'table')),
    title        text NOT NULL DEFAULT '',
    width        int NOT NULL DEFAULT 6 CHECK (width IN (3, 4, 6, 8, 12)),
    query        jsonb NOT NULL,
    no_period    boolean NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX bi_widgets_dashboard ON bi_widgets (dashboard_id, pos);

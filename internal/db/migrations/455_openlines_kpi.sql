-- Open lines (GO24DEV-694): KPI of the operators' answer time — the line's limits for the first answer and for the
-- answers to the next messages (minutes, 0 — off); who is told when a dialogue is late: its operator, the heads.
ALTER TABLE ol_widgets
    ADD COLUMN kpi_first int NOT NULL DEFAULT 0,
    ADD COLUMN kpi_further int NOT NULL DEFAULT 0,
    ADD COLUMN kpi_operator bool NOT NULL DEFAULT true,
    ADD COLUMN kpi_heads bool NOT NULL DEFAULT false;

-- the dialogue's violations: the first answer was late; how many waits were late; kpi_wait — the wait already counted
ALTER TABLE ol_sessions
    ADD COLUMN kpi_first_late bool NOT NULL DEFAULT false,
    ADD COLUMN kpi_late int NOT NULL DEFAULT 0,
    ADD COLUMN kpi_wait timestamptz;

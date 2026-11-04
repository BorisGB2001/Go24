-- «Учитывать время, затраченное на задачу» (ALLOW_TIME_TRACKING): stored inverted, so the tasks made
-- before, by robots, the import and the API keep the timer. time_over_at: when the responsible and the creator were
-- told that the logged time passed «Плановые трудозатраты» (once; a new estimate clears it).
ALTER TABLE tasks_all ADD COLUMN no_time_tracking boolean NOT NULL DEFAULT false,
    ADD COLUMN time_over_at timestamptz;
CREATE OR REPLACE VIEW tasks AS SELECT * FROM tasks_all WHERE deleted_at IS NULL; -- the new columns (see 060)
ALTER TABLE task_templates ADD COLUMN no_time_tracking boolean NOT NULL DEFAULT false,
    ADD COLUMN estimate_minutes integer;

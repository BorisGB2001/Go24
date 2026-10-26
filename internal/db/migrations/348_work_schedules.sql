-- «Графики работы» (Рабочее время): fixed, shift or flexible schedules of departments and people; the
-- violations of a working day are counted by the person's schedule, without one — by the company's working time.
CREATE TABLE work_schedules (
    id           bigserial PRIMARY KEY,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    kind         text NOT NULL CHECK (kind IN ('fixed', 'shift', 'flexible')),
    times        jsonb NOT NULL DEFAULT '[]', -- fixed: [{"days":[1,2,3,4,5],"start":"09:00","end":"18:00","break":60}]
    late_after   text NOT NULL DEFAULT '',    -- fixed: a day started later is late ('' — the start of the day)
    leave_before text NOT NULL DEFAULT '',    -- fixed: a day finished earlier is an early leave ('' — not checked)
    min_duration text NOT NULL DEFAULT '',    -- a shorter day (without pauses) is short ('' — not checked)
    tolerance    int NOT NULL DEFAULT 0 CHECK (tolerance BETWEEN 0 AND 720), -- shift: minutes allowed late or early
    created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE work_shifts (
    id          bigserial PRIMARY KEY,
    schedule_id bigint NOT NULL REFERENCES work_schedules(id) ON DELETE CASCADE,
    name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    start_at    text NOT NULL, -- "08:00"
    end_at      text NOT NULL, -- "20:00"; before the start — the shift ends the next day
    break_min   int NOT NULL DEFAULT 0,
    sort        int NOT NULL DEFAULT 0
);
CREATE INDEX work_shifts_schedule_idx ON work_shifts (schedule_id, sort);

-- members: departments (with their sub-departments) and people; excluded people are left out of the departments
CREATE TABLE work_schedule_members (
    schedule_id   bigint NOT NULL REFERENCES work_schedules(id) ON DELETE CASCADE,
    user_id       uuid REFERENCES users(id) ON DELETE CASCADE,
    department_id uuid REFERENCES departments(id) ON DELETE CASCADE,
    excluded      boolean NOT NULL DEFAULT false,
    CHECK ((user_id IS NULL) <> (department_id IS NULL))
);
CREATE UNIQUE INDEX work_schedule_members_idx ON work_schedule_members (schedule_id, coalesce(user_id, department_id));
CREATE INDEX work_schedule_members_user_idx ON work_schedule_members (user_id);
CREATE INDEX work_schedule_members_dept_idx ON work_schedule_members (department_id);

-- «График смен»: the shift of a person on a day
CREATE TABLE work_shift_plan (
    user_id  uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    day      date NOT NULL,
    shift_id bigint NOT NULL REFERENCES work_shifts(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, day)
);
CREATE INDEX work_shift_plan_shift_idx ON work_shift_plan (shift_id);

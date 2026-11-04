-- «Зарплата и отпуск» of the profile (GO24DEV-895): the vacation days an employee is entitled to in a
-- year (NULL — the company's default) and carried over from the last one; the payslips an HR officer uploads.
CREATE TABLE vacation_balances (
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    year       integer NOT NULL,
    days       integer CHECK (days BETWEEN 0 AND 366),
    carried    integer NOT NULL DEFAULT 0 CHECK (carried BETWEEN -366 AND 366),
    updated_by uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, year)
);

-- a payslip of a month (one per employee and month: a new upload replaces it)
CREATE TABLE payslips (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    month       date NOT NULL CHECK (extract(day FROM month) = 1),
    file_key    text NOT NULL,
    file_name   text NOT NULL,
    size        bigint NOT NULL DEFAULT 0,
    uploaded_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    seen_at     timestamptz,
    UNIQUE (user_id, month)
);

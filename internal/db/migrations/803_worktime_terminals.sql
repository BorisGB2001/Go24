-- «Терминал учёта времени».Time (GO24DEV-910): a tablet at the office entrance opens the terminal's secret
-- link, and employees start, pause and finish their working day there with a personal PIN. A PIN is kept as an HMAC
-- (unique: the PIN alone tells the person); wrong PINs lock the terminal for a while.
CREATE TABLE worktime_terminals (
    id           bigserial PRIMARY KEY,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    token        text NOT NULL UNIQUE,
    active       boolean NOT NULL DEFAULT true,
    fails        int NOT NULL DEFAULT 0,    -- wrong PINs in a row
    locked_until timestamptz,
    used_at      timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE worktime_pins (
    user_id  uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    pin_hash text NOT NULL UNIQUE,
    set_at   timestamptz NOT NULL DEFAULT now()
);
-- the terminal a day was started or finished at ('' — not at a terminal)
ALTER TABLE workdays ADD COLUMN start_terminal text NOT NULL DEFAULT '', ADD COLUMN end_terminal text NOT NULL DEFAULT '';

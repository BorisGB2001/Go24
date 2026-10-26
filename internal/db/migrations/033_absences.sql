-- «График отсутствий»: vacations, sick leave, business trips, days off.
CREATE TABLE absences (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind       text NOT NULL CHECK (kind IN ('vacation', 'sick', 'trip', 'dayoff', 'other')),
    starts_on  date NOT NULL,
    ends_on    date NOT NULL CHECK (ends_on >= starts_on),
    comment    text NOT NULL DEFAULT '',
    created_by uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX absences_range_idx ON absences (starts_on, ends_on);
CREATE INDEX absences_user_idx ON absences (user_id);

-- One's own calendars, as «Добавить календарь» (GO24DEV-423): besides the main personal calendar a person
-- keeps others (Рабочий, Личный…), each with its colour; a meeting of the person may be in one of them. The colour of
-- the main calendar is the person's choice too ("" — that of the theme). A calendar is deleted with its meetings.
CREATE TABLE user_calendars (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       text NOT NULL,
    color      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX user_calendars_owner ON user_calendars (owner_id);
ALTER TABLE events ADD COLUMN calendar_id uuid REFERENCES user_calendars(id) ON DELETE CASCADE;
CREATE INDEX events_calendar ON events (calendar_id) WHERE calendar_id IS NOT NULL;
ALTER TABLE users ADD COLUMN calendar_color text NOT NULL DEFAULT '';

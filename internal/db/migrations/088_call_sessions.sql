-- A call of a chat as its line in the chat shows it (GO24DEV-255): the LiveKit room and the chat, the line itself (a
-- Matrix message of whoever started the call, edited when it ends), who took part, when it began and ended.
CREATE TABLE call_sessions (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    lk_room    text NOT NULL,
    chat_room  text NOT NULL,
    event_id   text NOT NULL DEFAULT '',
    started_by uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    video      boolean NOT NULL DEFAULT false,
    people     uuid[] NOT NULL DEFAULT '{}',
    started_at timestamptz NOT NULL DEFAULT now(),
    ended_at   timestamptz
);
CREATE UNIQUE INDEX call_sessions_live ON call_sessions (lk_room) WHERE ended_at IS NULL;

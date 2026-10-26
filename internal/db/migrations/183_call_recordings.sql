-- A recording of a call (GO24DEV-454): one of the call starts it, LiveKit Egress records the room
-- into a file; when it is over, go24-worker puts the file on the Drive of the one who started it («Записи звонков»)
-- and tells the call's chat or task. One recording goes on in a room at a time; a stopped one
-- (stopped_at) stays active until its file is saved. call_query is the call as /call?…
-- has it (task=, event=, room=), lk_room — its LiveKit room. Whether calls may be recorded at all is the
-- administrators' setting company_settings 'call_recording'.
CREATE TABLE call_recordings (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    lk_room    text NOT NULL,
    title      text NOT NULL,
    call_query text NOT NULL,
    egress_id  text NOT NULL UNIQUE,
    audio      boolean NOT NULL DEFAULT false,
    started_by uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    stopped_by uuid REFERENCES users(id) ON DELETE SET NULL,
    stopped_at timestamptz,
    state      text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'done', 'failed')),
    file_id    uuid REFERENCES files(id) ON DELETE SET NULL,
    error      text NOT NULL DEFAULT '',
    started_at timestamptz NOT NULL DEFAULT now(),
    ended_at   timestamptz
);
CREATE UNIQUE INDEX call_recordings_active ON call_recordings (lk_room) WHERE state = 'active' AND stopped_at IS NULL;

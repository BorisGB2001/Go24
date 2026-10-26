-- Text of voice messages (Whisper), made once on request: the recording stays in Matrix, this is its index.
CREATE TABLE voice_transcripts (
    event_id   text PRIMARY KEY,
    room_id    text NOT NULL,
    body       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

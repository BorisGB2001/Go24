-- The transcript of a call's recording, as CoPilot's (GO24DEV-798): when the administrators let it
-- (company_settings 'call_transcribe'), a saved recording waits for go24-worker (transcript = 'pending'), which has
-- Whisper make its text into a file beside the recording on the Drive (transcript_file) and tells the call.
ALTER TABLE call_recordings
    ADD COLUMN transcript       text NOT NULL DEFAULT '' CHECK (transcript IN ('', 'pending', 'done', 'failed')),
    ADD COLUMN transcript_tries int NOT NULL DEFAULT 0,
    ADD COLUMN transcript_file  uuid REFERENCES files(id) ON DELETE SET NULL;
CREATE INDEX call_recordings_transcript ON call_recordings (ended_at) WHERE transcript = 'pending';

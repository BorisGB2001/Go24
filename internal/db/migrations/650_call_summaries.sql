-- «Итоги встречи» of a call's recording, as CoPilot's (GO24DEV-838): when the administrators let it
-- (company_settings 'call_summary'), the AI reads the transcript and writes the summary of the meeting — what it was
-- about, agreements, tasks, the next meeting; it is kept here and in a file beside the recording (summary_file).
ALTER TABLE call_recordings
    ADD COLUMN summary      text NOT NULL DEFAULT '',
    ADD COLUMN summary_file uuid REFERENCES files(id) ON DELETE SET NULL;

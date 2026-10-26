-- «Расшифровка звонков»: the text of a recorded conversation (or of the client's voicemail) made by
-- go24's Whisper — on request or by itself after the call (company_settings 'telephony_transcribe'); "" — none yet.
ALTER TABLE telephony_calls ADD COLUMN transcript text NOT NULL DEFAULT '';

-- «Открытые линии»: the text of a client's voice message or of any sound file of a dialogue, as the transcription of
-- audio messages (GO24DEV-997) — Whisper makes it when an operator presses «Текст», go24 keeps it.
ALTER TABLE ol_messages ADD COLUMN transcript text NOT NULL DEFAULT '';

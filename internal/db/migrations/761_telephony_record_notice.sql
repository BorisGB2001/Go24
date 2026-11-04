-- «Предупреждать о записи разговора»: when the conversations are recorded, a line may play its notice
-- (WAV for Asterisk) to the client — on an incoming call before it rings anyone, on an outgoing one when the client answers.
ALTER TABLE telephony_trunks ADD COLUMN rec_note_on boolean NOT NULL DEFAULT false;
ALTER TABLE telephony_trunks ADD COLUMN rec_note bytea;

-- «Оценка качества обслуживания»: a line may ask the client to rate the conversation when the employee
-- has hung up — rate_prompt is the invitation (WAV for Asterisk), the client presses 1–5; rating 0 — not rated.
ALTER TABLE telephony_trunks ADD COLUMN rate_on boolean NOT NULL DEFAULT false;
ALTER TABLE telephony_trunks ADD COLUMN rate_prompt bytea;
ALTER TABLE telephony_calls ADD COLUMN rating smallint NOT NULL DEFAULT 0 CHECK (rating BETWEEN 0 AND 5);

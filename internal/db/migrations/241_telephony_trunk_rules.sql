-- How a call to an external line is handled number settings (GO24DEV-538): the line's working hours
-- (work_on; days 1 = Monday … 7 = Sunday, from–to in the portal's time zone) and what happens to a call nobody
-- answered (noanswer) or out of the working hours (offhours): hang up, voicemail (greeting — the administrators'
-- recording, 8 kHz mono WAV; none — a beep only), or forward to a number (*_to: an employee's inner number or an
-- outside one, called through the same line).
ALTER TABLE telephony_trunks
    ADD COLUMN work_on     boolean NOT NULL DEFAULT false,
    ADD COLUMN work_days   integer[] NOT NULL DEFAULT '{1,2,3,4,5}',
    ADD COLUMN work_from   text NOT NULL DEFAULT '09:00',
    ADD COLUMN work_to     text NOT NULL DEFAULT '18:00',
    ADD COLUMN noanswer    text NOT NULL DEFAULT 'hangup' CHECK (noanswer IN ('hangup', 'voicemail', 'forward')),
    ADD COLUMN noanswer_to text NOT NULL DEFAULT '',
    ADD COLUMN offhours    text NOT NULL DEFAULT 'hangup' CHECK (offhours IN ('hangup', 'voicemail', 'forward')),
    ADD COLUMN offhours_to text NOT NULL DEFAULT '',
    ADD COLUMN greeting    bytea;

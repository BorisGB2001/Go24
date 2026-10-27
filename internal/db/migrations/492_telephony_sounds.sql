-- Sounds of the robots «Информационный звонок» (GO24DEV-725): the message (8 kHz mono WAV) played to the client, by
-- its checksum (md5, as the greetings of the lines); go24 writes it into Asterisk's configuration as greet-<sum>.wav
-- and drops the ones no robot plays any more.
CREATE TABLE telephony_sounds (
    sum        text PRIMARY KEY,
    data       bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- the robot kinds of the stream crm too (GO24DEV-734, its migration has 'infocall'): whichever is applied later keeps both
ALTER TABLE crm_robots DROP CONSTRAINT crm_robots_kind_check;
ALTER TABLE crm_robots ADD CONSTRAINT crm_robots_kind_check
    CHECK (kind IN ('task', 'notify', 'comment', 'activity', 'mail', 'responsible', 'stage', 'copy', 'move', 'booking',
        'fields', 'webhook', 'infocall', 'observers', 'document', 'bp', 'close'));

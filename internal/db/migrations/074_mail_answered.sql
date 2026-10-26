-- «Вы ответили на это письмо …» / «Вы переслали …», as Outlook shows over a letter and in the list (GO24DEV-136): what was
-- done to the letter — '' nothing, reply, fwd (from go24 or the server's \Answered and $Forwarded) — and when, if go24 did it.
ALTER TABLE mail_messages
    ADD COLUMN answered    text NOT NULL DEFAULT '' CHECK (answered IN ('', 'reply', 'fwd')),
    ADD COLUMN answered_at timestamptz;

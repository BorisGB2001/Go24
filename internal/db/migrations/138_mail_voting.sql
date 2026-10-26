-- Voting buttons, as Outlook's (GO24DEV-331): voting — the choices a letter asks to vote for («Утвердить;Отклонить»,
-- X-Go24-Voting), vote — the choice an answer votes for (X-Go24-Vote). The sender's «Отслеживание» counts the answers.
ALTER TABLE mail_messages
    ADD COLUMN voting text NOT NULL DEFAULT '',
    ADD COLUMN vote   text NOT NULL DEFAULT '';

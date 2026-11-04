-- «Напоминания» (nudges), as in Outlook (GO24DEV-911): old letters waiting for an answer come up over the inbox list —
-- one received the user has not answered, one sent nobody answered. nudges — «Показывать напоминания о письмах»;
-- nudge_off — «Закрыть» put the letter's nudge away for good.
ALTER TABLE mail_settings ADD COLUMN nudges boolean NOT NULL DEFAULT true;
ALTER TABLE mail_messages ADD COLUMN nudge_off boolean NOT NULL DEFAULT false;

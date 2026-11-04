-- A letter sent to the knowledge base, as Outlook's «Отправить в OneNote»: the page it became, shown on the letter
-- (target — «space/page»).
ALTER TABLE mail_letter_shares DROP CONSTRAINT mail_letter_shares_kind_check;
ALTER TABLE mail_letter_shares ADD CONSTRAINT mail_letter_shares_kind_check CHECK (kind IN ('post', 'chat', 'kb'));

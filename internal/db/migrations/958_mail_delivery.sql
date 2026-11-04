-- Mail: Outlook's «Параметры доставки» of a letter — «Направлять ответы на» (Reply-To, the addresses answers go to) and
-- «Срок действия истекает» (Expiry-Date, after it the letter is out of date: struck through in the list).
ALTER TABLE mail_messages ADD COLUMN replies_to text NOT NULL DEFAULT '', ADD COLUMN expires_at timestamptz;

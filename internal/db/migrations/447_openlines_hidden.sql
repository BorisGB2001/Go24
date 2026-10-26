-- «Скрытое сообщение» of the open lines: an operator's or a head's message in the dialogue that only
-- go24's people see — it goes neither to the widget nor to the visitor's chat.
ALTER TABLE ol_messages DROP CONSTRAINT ol_messages_kind_check;
ALTER TABLE ol_messages ADD CONSTRAINT ol_messages_kind_check
    CHECK (kind IN ('', 'welcome', 'offline', 'noanswer', 'close', 'note', 'hidden'));

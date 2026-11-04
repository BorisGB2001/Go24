-- «Автоматические сообщения» of an open line: what the client gets a while after their dialogue is
-- closed — [{"after_minutes": since the closing, "text": …}]; a dialogue closed while the line has them counts from
-- automsg_from, automsg_step of them are sent (NULL — none to send).
ALTER TABLE ol_widgets ADD COLUMN IF NOT EXISTS auto_msgs jsonb NOT NULL DEFAULT '[]';
ALTER TABLE ol_sessions ADD COLUMN IF NOT EXISTS automsg_from timestamptz,
    ADD COLUMN IF NOT EXISTS automsg_step int NOT NULL DEFAULT 0;
ALTER TABLE ol_messages DROP CONSTRAINT ol_messages_kind_check;
ALTER TABLE ol_messages ADD CONSTRAINT ol_messages_kind_check
    CHECK (kind IN ('', 'welcome', 'offline', 'noanswer', 'close', 'note', 'hidden', 'form', 'form_done', 'bot', 'booking',
        'booking_done', 'automsg'));

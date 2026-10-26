-- «CRM-форма в диалоге»: an operator sends a CRM form to the client in the dialogue; the client fills
-- it in the widget (or by the link in Telegram), and the cards it made are bound to the dialogue.
ALTER TABLE ol_messages
    ADD COLUMN form_id     uuid REFERENCES crm_forms(id) ON DELETE SET NULL,
    ADD COLUMN form_secret text NOT NULL DEFAULT '', -- the link's key: the filling belongs to this dialogue
    ADD COLUMN form_done   timestamptz;              -- when the client filled it
CREATE UNIQUE INDEX ol_messages_form_secret ON ol_messages (form_secret) WHERE form_secret <> '';

ALTER TABLE ol_messages DROP CONSTRAINT ol_messages_kind_check;
ALTER TABLE ol_messages ADD CONSTRAINT ol_messages_kind_check
    CHECK (kind IN ('', 'welcome', 'offline', 'noanswer', 'close', 'note', 'hidden', 'form', 'form_done'));

ALTER TABLE crm_form_results ADD COLUMN ol_session_id uuid REFERENCES ol_sessions(id) ON DELETE SET NULL;

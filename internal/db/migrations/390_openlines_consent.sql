-- «Согласие на обработку персональных данных» of a line (152-ФЗ): the widget asks the visitor to
-- accept the line's agreement before the first message or contact form; when and from where they accepted it.
ALTER TABLE ol_widgets ADD COLUMN consent_on boolean NOT NULL DEFAULT false,
    ADD COLUMN consent_text text NOT NULL DEFAULT '' CHECK (length(consent_text) <= 10000);
ALTER TABLE ol_sessions ADD COLUMN consent_at timestamptz,
    ADD COLUMN consent_ip text NOT NULL DEFAULT '';

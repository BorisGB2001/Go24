-- «Шаблоны писем CRM»: the subject and the text (HTML) of a letter to a client, with the card's
-- fields in braces ({Title}, {Amount}, own fields by their names) as the document templates have them; for one kind
-- of card; shared — every employee picks it in the card's «Письмо», else only its author.
CREATE TABLE crm_mail_templates (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL,
    entity     text NOT NULL,
    subject    text NOT NULL DEFAULT '',
    body       text NOT NULL DEFAULT '',
    shared     bool NOT NULL DEFAULT true,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX crm_mail_templates_entity ON crm_mail_templates (entity, lower(name));

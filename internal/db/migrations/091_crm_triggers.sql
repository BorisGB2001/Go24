-- «Триггеры» of the portal CRM: an event of the client (a letter, the site's form, a message in the open line, a
-- deed done, an invoice paid, a quote accepted) moves a deal or a lead on to the stage the trigger stands on.
CREATE TABLE crm_triggers (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    stage_id   uuid NOT NULL REFERENCES crm_stages(id) ON DELETE CASCADE,
    kind       text NOT NULL CHECK (kind IN ('mail', 'form', 'openline', 'activity', 'invoice_paid', 'quote_accepted')),
    created_by uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (stage_id, kind)
);

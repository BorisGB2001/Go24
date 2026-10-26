-- «Дела» of the portal CRM: a call, a meeting or a to-do on a card, with a deadline and a responsible person.
-- Open ones are planned on the card and counted in «Мои дела»; a done one goes to the card's timeline.
CREATE TABLE crm_activities (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    entity         text NOT NULL CHECK (entity IN ('company', 'contact', 'deal', 'lead')),
    entity_id      uuid NOT NULL,
    kind           text NOT NULL DEFAULT 'todo' CHECK (kind IN ('todo', 'call', 'meeting')),
    subject        text NOT NULL CHECK (length(subject) BETWEEN 1 AND 300),
    description    text NOT NULL DEFAULT '' CHECK (length(description) <= 10000),
    deadline       timestamptz NOT NULL,
    responsible_id uuid NOT NULL REFERENCES users(id),
    done_at        timestamptz,
    reminded_at    timestamptz, -- the reminder before the deadline was sent
    created_by     uuid NOT NULL REFERENCES users(id),
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX crm_activities_card_idx ON crm_activities (entity, entity_id);
CREATE INDEX crm_activities_open_idx ON crm_activities (responsible_id, deadline) WHERE done_at IS NULL;

-- a deleted card takes its activities along
CREATE FUNCTION crm_activities_gone() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM crm_activities WHERE entity = TG_ARGV[0] AND entity_id = OLD.id;
    RETURN NULL;
END $$;
CREATE TRIGGER crm_deals_activities AFTER DELETE ON crm_deals FOR EACH ROW EXECUTE FUNCTION crm_activities_gone('deal');
CREATE TRIGGER crm_leads_activities AFTER DELETE ON crm_leads FOR EACH ROW EXECUTE FUNCTION crm_activities_gone('lead');
CREATE TRIGGER crm_contacts_activities AFTER DELETE ON crm_contacts FOR EACH ROW EXECUTE FUNCTION crm_activities_gone('contact');
CREATE TRIGGER crm_companies_activities AFTER DELETE ON crm_companies FOR EACH ROW EXECUTE FUNCTION crm_activities_gone('company');

ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check
    CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot', 'document', 'mail', 'activity'));

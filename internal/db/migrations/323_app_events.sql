-- Events of local applications, as event.bind (GO24DEV-604): an application subscribes a handler on its
-- own host to an event of go24; database triggers queue a delivery for each subscription (whatever path changed the
-- data — the UI, REST, a robot), the worker posts it with the tokens of the employee who bound it and retries.
-- The event tells only what changed (the ID), the application reads the rest over REST with its rights.
CREATE TABLE app_events (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    app_id      uuid NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    event       text NOT NULL,
    handler_url text NOT NULL CHECK (handler_url ~ '^https?://'),
    user_id     uuid REFERENCES users(id) ON DELETE SET NULL, -- whose tokens the handler gets (auth_type)
    last_at     timestamptz,
    last_status int,
    last_error  text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (app_id, event, handler_url)
);
CREATE INDEX app_events_event ON app_events (event);
CREATE TABLE app_event_deliveries (
    id         bigserial PRIMARY KEY,
    event_id   uuid NOT NULL REFERENCES app_events(id) ON DELETE CASCADE,
    event      text NOT NULL,
    data       jsonb NOT NULL,
    attempts   int NOT NULL DEFAULT 0,
    next_at    timestamptz NOT NULL DEFAULT now(),
    done_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX app_event_deliveries_due ON app_event_deliveries (next_at) WHERE done_at IS NULL;

-- go24_app_event queues the event for the subscriptions of installed applications; the same event of the same
-- record not yet sent is not queued twice (one save is often several updates), nor an update of a record whose
-- addition is not yet sent to the application.
CREATE FUNCTION go24_app_event(p_event text, p_data jsonb) RETURNS void AS $$
    INSERT INTO app_event_deliveries (event_id, event, data)
    SELECT e.id, p_event, p_data FROM app_events e JOIN apps a ON a.id = e.app_id
    WHERE e.event = p_event AND a.installed_at IS NOT NULL AND NOT go24_quiet()
      AND NOT EXISTS (SELECT 1 FROM app_event_deliveries d JOIN app_events x ON x.id = d.event_id
          WHERE x.app_id = e.app_id AND d.done_at IS NULL AND d.attempts = 0 AND d.data = p_data
            AND (d.event = p_event AND d.event_id = e.id OR p_event LIKE '%UPDATE' AND d.event = replace(p_event, 'UPDATE', 'ADD')))
$$ LANGUAGE sql;

-- CRM: ONCRM<ENTITY>ADD / UPDATE / DELETE with data.FIELDS.ID; a card of a smart process is ONCRMDYNAMICITEM… with
-- its ENTITY_TYPE_ID.
CREATE FUNCTION go24_app_event_crm() RETURNS trigger AS $$
DECLARE
    r       record;
    entity  text := TG_ARGV[0];
    op      text := CASE TG_OP WHEN 'INSERT' THEN 'ADD' WHEN 'UPDATE' THEN 'UPDATE' ELSE 'DELETE' END;
    fields  jsonb;
    type_id uuid;
BEGIN
    IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF;
    IF TG_OP = 'UPDATE' AND to_jsonb(NEW) = to_jsonb(OLD) THEN
        RETURN NULL;
    END IF;
    fields := jsonb_build_object('ID', r.id);
    IF entity = 'DEAL' THEN
        SELECT p.type_id INTO type_id FROM crm_pipelines p WHERE p.id = r.pipeline_id;
        IF type_id IS NOT NULL THEN
            entity := 'DYNAMICITEM';
            fields := fields || jsonb_build_object('ENTITY_TYPE_ID', type_id);
        END IF;
    END IF;
    PERFORM go24_app_event('ONCRM' || entity || op, jsonb_build_object('FIELDS', fields));
    RETURN NULL;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER crm_leads_app_event AFTER INSERT OR UPDATE OR DELETE ON crm_leads FOR EACH ROW EXECUTE FUNCTION go24_app_event_crm('LEAD');
CREATE TRIGGER crm_deals_app_event AFTER INSERT OR UPDATE OR DELETE ON crm_deals FOR EACH ROW EXECUTE FUNCTION go24_app_event_crm('DEAL');
CREATE TRIGGER crm_contacts_app_event AFTER INSERT OR UPDATE OR DELETE ON crm_contacts FOR EACH ROW EXECUTE FUNCTION go24_app_event_crm('CONTACT');
CREATE TRIGGER crm_companies_app_event AFTER INSERT OR UPDATE OR DELETE ON crm_companies FOR EACH ROW EXECUTE FUNCTION go24_app_event_crm('COMPANY');

-- Tasks: ONTASKADD / ONTASKUPDATE with data.FIELDS_AFTER.ID, ONTASKDELETE (to the trash; subtasks go with it) with
-- data.FIELDS_BEFORE.ID; the ID is the task's key, as REST has it.
CREATE FUNCTION go24_app_event_task() RETURNS trigger AS $$
DECLARE
    r   record;
    key text;
BEGIN
    IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF;
    key := (SELECT p.key FROM projects p WHERE p.id = r.project_id) || '-' || r.number;
    IF TG_OP = 'INSERT' THEN
        PERFORM go24_app_event('ONTASKADD', jsonb_build_object('FIELDS_AFTER', jsonb_build_object('ID', key)));
    ELSIF TG_OP = 'DELETE' AND OLD.deleted_at IS NULL OR TG_OP = 'UPDATE' AND NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL AND NEW.id = NEW.deleted_root THEN
        PERFORM go24_app_event('ONTASKDELETE', jsonb_build_object('FIELDS_BEFORE', jsonb_build_object('ID', key)));
    ELSIF TG_OP = 'UPDATE' AND NEW.deleted_at IS NULL AND OLD.deleted_at IS NULL AND to_jsonb(NEW) <> to_jsonb(OLD) THEN
        PERFORM go24_app_event('ONTASKUPDATE', jsonb_build_object('FIELDS_AFTER', jsonb_build_object('ID', key)));
    END IF;
    RETURN NULL;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER tasks_app_event AFTER INSERT OR UPDATE OR DELETE ON tasks_all FOR EACH ROW EXECUTE FUNCTION go24_app_event_task();

-- Employees: ONUSERADD with data.ID.
CREATE FUNCTION go24_app_event_user() RETURNS trigger AS $$
BEGIN
    PERFORM go24_app_event('ONUSERADD', jsonb_build_object('ID', NEW.id));
    RETURN NULL;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER users_app_event AFTER INSERT ON users FOR EACH ROW EXECUTE FUNCTION go24_app_event_user();

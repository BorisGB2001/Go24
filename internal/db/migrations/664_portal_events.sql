-- REST compatible with the portal, events (GO24DEV-835): a handler bound by the name (/rest/event.bind) gets
-- the event posts it — a form of PHP arrays with the records' numbers and «auth» for /rest/ (app_events.portal).
-- The events keep the number beside the go24 id (Portal_ID): a deleted record's number cannot be read when it is sent.
ALTER TABLE app_events ADD COLUMN portal boolean NOT NULL DEFAULT false;

CREATE OR REPLACE FUNCTION go24_app_event_crm() RETURNS trigger AS $$
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
    fields := jsonb_build_object('ID', r.id, 'Portal_ID', r.num);
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

CREATE OR REPLACE FUNCTION go24_app_event_task() RETURNS trigger AS $$
DECLARE
    r   record;
    key text;
    ids jsonb;
BEGIN
    IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF;
    key := (SELECT p.key FROM projects p WHERE p.id = r.project_id) || '-' || r.number;
    ids := jsonb_build_object('ID', key, 'Portal_ID', r.num);
    IF TG_OP = 'INSERT' THEN
        PERFORM go24_app_event('ONTASKADD', jsonb_build_object('FIELDS_AFTER', ids));
    ELSIF TG_OP = 'DELETE' AND OLD.deleted_at IS NULL OR TG_OP = 'UPDATE' AND NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL AND NEW.id = NEW.deleted_root THEN
        PERFORM go24_app_event('ONTASKDELETE', jsonb_build_object('FIELDS_BEFORE', ids));
    ELSIF TG_OP = 'UPDATE' AND NEW.deleted_at IS NULL AND OLD.deleted_at IS NULL AND to_jsonb(NEW) <> to_jsonb(OLD) THEN
        PERFORM go24_app_event('ONTASKUPDATE', jsonb_build_object('FIELDS_AFTER', ids));
    END IF;
    RETURN NULL;
END $$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION go24_app_event_user() RETURNS trigger AS $$
BEGIN
    PERFORM go24_app_event('ONUSERADD', jsonb_build_object('ID', NEW.id, 'Portal_ID', NEW.num));
    RETURN NULL;
END $$ LANGUAGE plpgsql;

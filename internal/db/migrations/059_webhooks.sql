-- Outgoing webhooks (for n8n and the like): an administrator subscribes a URL to events; database triggers queue a
-- delivery for each matching event (whatever path changed the data — the UI, the API, a robot), the worker sends it
-- signed (X-Go24-Signature: sha256=HMAC of the body) and retries.
CREATE TABLE webhooks (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    url         text NOT NULL CHECK (url ~ '^https?://'),
    events      text[] NOT NULL,
    secret      text NOT NULL,
    active      boolean NOT NULL DEFAULT true,
    last_at     timestamptz,
    last_status int,
    last_error  text NOT NULL DEFAULT '',
    created_by  uuid NOT NULL REFERENCES users(id),
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE webhook_deliveries (
    id         bigserial PRIMARY KEY,
    webhook_id uuid NOT NULL REFERENCES webhooks(id) ON DELETE CASCADE,
    event      text NOT NULL,
    payload    jsonb NOT NULL,
    attempts   int NOT NULL DEFAULT 0,
    next_at    timestamptz NOT NULL DEFAULT now(),
    done_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhook_deliveries_due_idx ON webhook_deliveries (next_at) WHERE done_at IS NULL;

CREATE FUNCTION go24_webhook(p_event text, p_data jsonb) RETURNS void AS $$
    INSERT INTO webhook_deliveries (webhook_id, event, payload)
    SELECT w.id, p_event, jsonb_build_object('event', p_event, 'at', now(), 'data', p_data)
    FROM webhooks w WHERE w.active AND p_event = ANY(w.events) AND NOT go24_quiet() -- imports stay silent
$$ LANGUAGE sql;

CREATE FUNCTION go24_webhook_deal() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        PERFORM go24_webhook('deal.created', to_jsonb(NEW) - 'comment');
    ELSIF NEW.stage_id IS DISTINCT FROM OLD.stage_id THEN
        PERFORM go24_webhook('deal.stage', (to_jsonb(NEW) - 'comment') || jsonb_build_object('old_stage_id', OLD.stage_id,
            'stage_code', (SELECT code FROM crm_stages WHERE id = NEW.stage_id), 'stage_semantic', (SELECT semantic FROM crm_stages WHERE id = NEW.stage_id)));
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER crm_deals_webhook AFTER INSERT OR UPDATE ON crm_deals FOR EACH ROW EXECUTE FUNCTION go24_webhook_deal();

CREATE FUNCTION go24_webhook_lead() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        PERFORM go24_webhook('lead.created', to_jsonb(NEW));
    ELSIF NEW.stage_id IS DISTINCT FROM OLD.stage_id THEN
        PERFORM go24_webhook('lead.stage', to_jsonb(NEW) || jsonb_build_object('old_stage_id', OLD.stage_id,
            'stage_code', (SELECT code FROM crm_stages WHERE id = NEW.stage_id)));
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER crm_leads_webhook AFTER INSERT OR UPDATE ON crm_leads FOR EACH ROW EXECUTE FUNCTION go24_webhook_lead();

CREATE FUNCTION go24_webhook_task() RETURNS trigger AS $$
DECLARE
    data jsonb;
BEGIN
    data := jsonb_build_object('id', NEW.id, 'key', (SELECT key FROM projects WHERE id = NEW.project_id) || '-' || NEW.number,
        'title', NEW.title, 'status', NEW.status, 'responsible_id', NEW.responsible_id, 'creator_id', NEW.creator_id, 'deadline', NEW.deadline);
    IF TG_OP = 'INSERT' THEN
        PERFORM go24_webhook('task.created', data);
    ELSIF NEW.status IS DISTINCT FROM OLD.status AND NEW.status = 'completed' THEN
        PERFORM go24_webhook('task.completed', data);
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER tasks_webhook AFTER INSERT OR UPDATE ON tasks FOR EACH ROW EXECUTE FUNCTION go24_webhook_task();

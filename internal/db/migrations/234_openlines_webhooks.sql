-- Outgoing webhooks of the open lines, as OnSessionStart, OnOpenLineMessageAdd and OnSessionFinish of the -- imopenlines: a dialogue started (the visitor's first message), a message of the visitor or an operator (not the
-- line's automatic ones nor service lines), a dialogue closed (also as spam).
CREATE FUNCTION go24_webhook_ol_message() RETURNS trigger AS $$
BEGIN
    IF NEW.kind = '' THEN
        PERFORM go24_webhook('openline.message', jsonb_build_object('id', NEW.id, 'dialog_id', NEW.session_id,
            'line_id', (SELECT widget_id FROM ol_sessions WHERE id = NEW.session_id),
            'from', CASE WHEN NEW.author_id IS NULL THEN 'visitor' ELSE 'operator' END, 'operator_id', NEW.author_id,
            'text', NEW.body, 'at', NEW.at));
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER ol_messages_webhook AFTER INSERT ON ol_messages FOR EACH ROW EXECUTE FUNCTION go24_webhook_ol_message();

CREATE FUNCTION go24_webhook_ol_session() RETURNS trigger AS $$
DECLARE
    data jsonb;
BEGIN
    data := jsonb_build_object('id', NEW.id, 'line_id', NEW.widget_id, 'operator_id', NEW.operator_id, 'name', NEW.name,
        'phone', NEW.phone, 'email', NEW.email, 'page', NEW.page, 'lead_id', NEW.lead_id, 'contact_id', NEW.contact_id);
    IF NEW.started_at IS NOT NULL AND OLD.started_at IS NULL THEN
        PERFORM go24_webhook('openline.started', data);
    END IF;
    IF NEW.status = 'closed' AND OLD.status = 'open' THEN
        PERFORM go24_webhook('openline.closed', data || jsonb_build_object('spam', NEW.spam, 'vote', NEW.vote,
            'started_at', NEW.started_at, 'answered_at', NEW.answered_at, 'closed_at', NEW.closed_at));
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER ol_sessions_webhook AFTER UPDATE ON ol_sessions FOR EACH ROW EXECUTE FUNCTION go24_webhook_ol_session();

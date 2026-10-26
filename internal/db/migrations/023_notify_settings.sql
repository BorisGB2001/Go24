-- Which notifications a person wants (the portal «Настройки уведомлений») and whether to get them in the messenger
-- too (a private chat with the go24 bot: Element on the phone shows them as push notifications).
ALTER TABLE users ADD COLUMN notify_off text[] NOT NULL DEFAULT '{}',   -- kinds turned off
                  ADD COLUMN notify_matrix boolean NOT NULL DEFAULT false;
ALTER TABLE notifications ADD COLUMN pushed boolean NOT NULL DEFAULT false; -- sent to the messenger
CREATE INDEX notifications_push_idx ON notifications (id) WHERE NOT pushed;
ALTER TABLE matrix_rooms DROP CONSTRAINT matrix_rooms_kind_check;
ALTER TABLE matrix_rooms ADD CONSTRAINT matrix_rooms_kind_check CHECK (kind IN ('space', 'project', 'task', 'feed', 'notify'));

-- a kind the person turned off is not written at all
CREATE FUNCTION go24_notification_filter() RETURNS trigger AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM users WHERE id = NEW.user_id AND NEW.kind = ANY(notify_off)) THEN
        RETURN NULL;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER notifications_filter BEFORE INSERT ON notifications FOR EACH ROW EXECUTE FUNCTION go24_notification_filter();

-- the messenger delivery wakes up on new notifications of people who want them there
CREATE FUNCTION go24_notification_push() RETURNS trigger AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM users WHERE id = NEW.user_id AND notify_matrix) THEN
        PERFORM pg_notify('go24_push', NEW.id::text);
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER notifications_push AFTER INSERT ON notifications FOR EACH ROW EXECUTE FUNCTION go24_notification_push();

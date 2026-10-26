-- The person acting for someone who is away gets a copy of that person's notifications (power «notify»).
ALTER TABLE notifications ADD COLUMN for_user uuid REFERENCES users(id) ON DELETE CASCADE; -- a copy: whose notification it was

CREATE FUNCTION go24_notification_acting() RETURNS trigger AS $$
BEGIN
    IF NEW.for_user IS NULL THEN
        INSERT INTO notifications (user_id, actor_id, kind, task_id, post_id, event_id, body, for_user)
        SELECT a.acting_id, NEW.actor_id, NEW.kind, NEW.task_id, NEW.post_id, NEW.event_id, NEW.body, NEW.user_id
        FROM absences a
        WHERE a.user_id = NEW.user_id AND a.acting_id IS NOT NULL AND a.acting_id IS DISTINCT FROM NEW.actor_id
          AND 'notify' = ANY(a.acting_rights) AND current_date BETWEEN a.starts_on AND a.ends_on;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER notifications_acting AFTER INSERT ON notifications FOR EACH ROW EXECUTE FUNCTION go24_notification_acting();

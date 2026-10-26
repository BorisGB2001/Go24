-- «Уведомления» (the portal bell): what happened to a person's tasks and posts. Rows are written by triggers, so
-- every path (go24, the messenger bridge, the API) notifies the same way; imports set go24.quiet to stay silent.
CREATE TABLE notifications (
    id         bigserial PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE, -- who is notified
    actor_id   uuid REFERENCES users(id) ON DELETE SET NULL,         -- who did it
    kind       text NOT NULL, -- task_new | comment | status | deadline | responsible | participant | post_comment | like | important
    task_id    uuid REFERENCES tasks(id) ON DELETE CASCADE,
    post_id    uuid REFERENCES feed_posts(id) ON DELETE CASCADE,
    body       text NOT NULL DEFAULT '', -- comment excerpt, new status, new deadline, role
    created_at timestamptz NOT NULL DEFAULT now(),
    read_at    timestamptz
);
CREATE INDEX notifications_user_idx ON notifications (user_id, id DESC);
CREATE INDEX notifications_unread_idx ON notifications (user_id) WHERE read_at IS NULL;

-- live bell: "n<user>:<id>" goes to the user's open pages
CREATE FUNCTION go24_notification_notify() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('go24_notify', 'n' || NEW.user_id::text || ':' || NEW.id::text);
    RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER notifications_notify AFTER INSERT ON notifications FOR EACH ROW EXECUTE FUNCTION go24_notification_notify();

CREATE FUNCTION go24_quiet() RETURNS boolean AS $$
    SELECT coalesce(current_setting('go24.quiet', true), '') = 'on'
$$ LANGUAGE sql STABLE;

-- task members except the actor: creator, responsible, accomplices, auditors
CREATE FUNCTION go24_notify_task(p_task uuid, p_actor uuid, p_kind text, p_body text) RETURNS void AS $$
    INSERT INTO notifications (user_id, actor_id, kind, task_id, body)
    SELECT DISTINCT m.uid, p_actor, p_kind, p_task, left(p_body, 300) FROM (
        SELECT creator_id AS uid FROM tasks WHERE id = p_task
        UNION SELECT responsible_id FROM tasks WHERE id = p_task
        UNION SELECT user_id FROM task_participants WHERE task_id = p_task) m
    JOIN users u ON u.id = m.uid AND u.active
    WHERE m.uid IS DISTINCT FROM p_actor
$$ LANGUAGE sql;

CREATE FUNCTION go24_task_comment_notification() RETURNS trigger AS $$
BEGIN
    IF NEW.legacy_id IS NULL AND NOT go24_quiet() THEN
        PERFORM go24_notify_task(NEW.task_id, NEW.author_id, 'comment', NEW.body);
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER task_comments_notification AFTER INSERT ON task_comments
    FOR EACH ROW EXECUTE FUNCTION go24_task_comment_notification();

CREATE FUNCTION go24_task_history_notification() RETURNS trigger AS $$
DECLARE resp uuid;
BEGIN
    IF go24_quiet() THEN
        RETURN NEW;
    END IF;
    CASE NEW.field
    WHEN 'created' THEN
        PERFORM go24_notify_task(NEW.task_id, NEW.actor_id, 'task_new', '');
    WHEN 'status' THEN
        PERFORM go24_notify_task(NEW.task_id, NEW.actor_id, 'status', NEW.new_value);
    WHEN 'deadline' THEN
        PERFORM go24_notify_task(NEW.task_id, NEW.actor_id, 'deadline', NEW.new_value);
    WHEN 'responsible' THEN -- the task row is already updated: tell the new responsible person
        SELECT responsible_id INTO resp FROM tasks WHERE id = NEW.task_id;
        INSERT INTO notifications (user_id, actor_id, kind, task_id)
        SELECT resp, NEW.actor_id, 'responsible', NEW.task_id WHERE resp IS DISTINCT FROM NEW.actor_id;
    ELSE
    END CASE;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER task_history_notification AFTER INSERT ON task_history
    FOR EACH ROW EXECUTE FUNCTION go24_task_history_notification();

CREATE FUNCTION go24_feed_comment_notification() RETURNS trigger AS $$
BEGIN
    IF NOT go24_quiet() THEN
        INSERT INTO notifications (user_id, actor_id, kind, post_id, body)
        SELECT p.author_id, NEW.author_id, 'post_comment', p.id, left(NEW.body, 300)
        FROM feed_posts p WHERE p.id = NEW.post_id AND p.author_id <> NEW.author_id;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER feed_comments_notification AFTER INSERT ON feed_comments
    FOR EACH ROW EXECUTE FUNCTION go24_feed_comment_notification();

CREATE FUNCTION go24_feed_like_notification() RETURNS trigger AS $$
BEGIN
    IF NOT go24_quiet() THEN
        INSERT INTO notifications (user_id, actor_id, kind, post_id)
        SELECT p.author_id, NEW.user_id, 'like', p.id
        FROM feed_posts p WHERE p.id = NEW.post_id AND p.author_id <> NEW.user_id;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER feed_likes_notification AFTER INSERT ON feed_likes
    FOR EACH ROW EXECUTE FUNCTION go24_feed_like_notification();

-- an important post is announced to everyone it is addressed to
CREATE FUNCTION go24_feed_post_notification() RETURNS trigger AS $$
BEGIN
    IF NEW.important AND NOT go24_quiet() THEN
        INSERT INTO notifications (user_id, actor_id, kind, post_id, body)
        SELECT u.id, NEW.author_id, 'important', NEW.id, left(coalesce(nullif(NEW.title, ''), NEW.body), 300)
        FROM users u
        WHERE u.active AND u.id <> NEW.author_id
          AND (NEW.project_id IS NULL OR u.id IN (SELECT user_id FROM project_members WHERE project_id = NEW.project_id));
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER feed_posts_notification AFTER INSERT ON feed_posts
    FOR EACH ROW EXECUTE FUNCTION go24_feed_post_notification();

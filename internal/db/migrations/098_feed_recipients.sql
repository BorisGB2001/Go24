-- Addressees of a post, as «Кому» of the portal live feed (GO24DEV-271): chosen employees and departments (with
-- their sub-departments, now and later). Such a post (limited) is seen by them and its author only; they are told
-- by go24 itself, after the addressees are written.
ALTER TABLE feed_posts ADD COLUMN limited boolean NOT NULL DEFAULT false;
CREATE TABLE feed_post_recipients (
    post_id       uuid NOT NULL REFERENCES feed_posts(id) ON DELETE CASCADE,
    user_id       uuid REFERENCES users(id) ON DELETE CASCADE,
    department_id uuid REFERENCES departments(id) ON DELETE CASCADE,
    CHECK ((user_id IS NULL) <> (department_id IS NULL))
);
CREATE INDEX feed_post_recipients_post_idx ON feed_post_recipients (post_id);

-- go24_feed_recipient: the person is an addressee of the post — by name or through a department they are in, or a
-- department above it.
CREATE FUNCTION go24_feed_recipient(p_post uuid, p_user uuid) RETURNS boolean AS $$
    SELECT EXISTS (SELECT 1 FROM feed_post_recipients r WHERE r.post_id = p_post AND (r.user_id = p_user OR r.department_id IN (
        WITH RECURSIVE up AS (
            SELECT department_id AS id FROM user_departments WHERE user_id = p_user
            UNION SELECT d.parent_id FROM departments d JOIN up ON d.id = up.id WHERE d.parent_id IS NOT NULL)
        SELECT id FROM up)))
$$ LANGUAGE sql STABLE;

-- an important post is announced to everyone it is addressed to (a limited one — by go24, to its addressees)
CREATE OR REPLACE FUNCTION go24_feed_post_notification() RETURNS trigger AS $$
BEGIN
    IF NEW.important AND NOT NEW.limited AND NOT go24_quiet() THEN
        INSERT INTO notifications (user_id, actor_id, kind, post_id, body)
        SELECT u.id, NEW.author_id, 'important', NEW.id, left(coalesce(nullif(NEW.title, ''), NEW.body), 300)
        FROM users u
        WHERE u.active AND u.id <> NEW.author_id
          AND (NEW.project_id IS NULL OR u.id IN (SELECT user_id FROM project_members WHERE project_id = NEW.project_id));
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;

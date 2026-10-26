-- Task marks: tags (shared), and the person's own favourites, pins and «выключить звук».
CREATE TABLE task_tags (
    task_id uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    tag     text NOT NULL CHECK (length(tag) BETWEEN 1 AND 50),
    PRIMARY KEY (task_id, tag)
);
CREATE INDEX task_tags_tag_idx ON task_tags (lower(tag));

CREATE TABLE task_marks (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    task_id uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    mark    text NOT NULL CHECK (mark IN ('favorite', 'pin', 'mute')),
    PRIMARY KEY (user_id, task_id, mark)
);
CREATE INDEX task_marks_task_idx ON task_marks (task_id);

-- a muted task notifies nothing but mentions
CREATE OR REPLACE FUNCTION go24_notification_filter() RETURNS trigger AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM users WHERE id = NEW.user_id AND NEW.kind = ANY(notify_off)) THEN
        RETURN NULL;
    END IF;
    IF NEW.task_id IS NOT NULL AND NEW.kind <> 'mention'
       AND EXISTS (SELECT 1 FROM task_marks WHERE user_id = NEW.user_id AND task_id = NEW.task_id AND mark = 'mute') THEN
        RETURN NULL;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;

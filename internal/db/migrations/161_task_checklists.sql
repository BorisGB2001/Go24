-- Several checklists in a task: each has its name (empty = «Чек-лист N» in the page's language)
-- and its order; an item belongs to one of them and can be marked important. Items added without a checklist
-- (robots, the portal import, older code) go to the task's first one, made on the spot.
CREATE TABLE task_checklists (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id    uuid NOT NULL REFERENCES tasks_all(id) ON DELETE CASCADE,
    title      text NOT NULL DEFAULT '' CHECK (length(title) <= 200),
    sort       integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX task_checklists_task_idx ON task_checklists (task_id, sort);

ALTER TABLE task_checklist_items
    ADD COLUMN checklist_id uuid REFERENCES task_checklists(id) ON DELETE CASCADE,
    ADD COLUMN important boolean NOT NULL DEFAULT false;

INSERT INTO task_checklists (task_id, sort) SELECT DISTINCT task_id, 1 FROM task_checklist_items;
UPDATE task_checklist_items i SET checklist_id = c.id FROM task_checklists c WHERE c.task_id = i.task_id;
ALTER TABLE task_checklist_items ALTER COLUMN checklist_id SET NOT NULL;
CREATE INDEX task_checklist_items_list_idx ON task_checklist_items (checklist_id, sort);

CREATE FUNCTION task_checklist_item_list() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.checklist_id IS NULL THEN
        SELECT id INTO NEW.checklist_id FROM task_checklists WHERE task_id = NEW.task_id ORDER BY sort, created_at LIMIT 1;
        IF NEW.checklist_id IS NULL THEN
            INSERT INTO task_checklists (task_id, sort) VALUES (NEW.task_id, 1) RETURNING id INTO NEW.checklist_id;
        END IF;
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER task_checklist_item_list BEFORE INSERT ON task_checklist_items
    FOR EACH ROW EXECUTE FUNCTION task_checklist_item_list();

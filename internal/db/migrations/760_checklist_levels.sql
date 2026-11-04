-- Tasks: nested checklist items and the people of an item. level — the item's depth in its list
-- (0 — top; an item of level n+1 belongs to the nearest item above it of level n); members — people mentioned
-- as «@Name» in the item (they are accomplices of the task too), shown at the item.
ALTER TABLE task_checklist_items ADD COLUMN level smallint NOT NULL DEFAULT 0 CHECK (level BETWEEN 0 AND 4);

CREATE TABLE task_checklist_item_members (
    item_id uuid NOT NULL REFERENCES task_checklist_items(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (item_id, user_id)
);

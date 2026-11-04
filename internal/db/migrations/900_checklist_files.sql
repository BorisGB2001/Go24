-- Files of a checklist item (GO24DEV-936): uploaded from the computer or taken from the drive into
-- an item, shown under it and not among the task's attachments. A file of an item keeps its task_id (who sees the
-- task reads it); a deleted item leaves its files to the task.
ALTER TABLE files ADD COLUMN checklist_item_id uuid REFERENCES task_checklist_items(id) ON DELETE SET NULL;
CREATE INDEX files_checklist_item_idx ON files (checklist_item_id) WHERE checklist_item_id IS NOT NULL;

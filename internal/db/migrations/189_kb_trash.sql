-- «Корзина» of the knowledge base: a deleted page goes to the trash with its subpages, a deleted base with
-- all its pages; either comes back as it was or is deleted for good — by hand or after the days of the company
-- setting of the trash (task_trash_days, as tasks and the Drive).
--
-- As with tasks (060), the tables become kb_pages_all and kb_spaces_all, and kb_pages / kb_spaces are views of what is
-- not in the trash, so every query keeps seeing only live pages and bases; the views are auto-updatable. A new column
-- is added to the table and the view is made again («SELECT *» is expanded when a view is made).
ALTER TABLE kb_pages RENAME TO kb_pages_all;
ALTER TABLE kb_pages_all
    ADD COLUMN deleted_at   timestamptz,
    ADD COLUMN deleted_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN deleted_root uuid; -- the page the user deleted: its subpages go to the trash and come back with it
CREATE INDEX kb_pages_all_trash_idx ON kb_pages_all (deleted_root) WHERE deleted_at IS NOT NULL;
CREATE VIEW kb_pages AS SELECT * FROM kb_pages_all WHERE deleted_at IS NULL;

ALTER TABLE kb_spaces RENAME TO kb_spaces_all;
ALTER TABLE kb_spaces_all
    ADD COLUMN deleted_at timestamptz,
    ADD COLUMN deleted_by uuid REFERENCES users(id) ON DELETE SET NULL;
CREATE VIEW kb_spaces AS SELECT * FROM kb_spaces_all WHERE deleted_at IS NULL;

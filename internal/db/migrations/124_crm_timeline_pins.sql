-- a card's timeline: any line can be pinned above the history, a comment can be edited
ALTER TABLE crm_timeline ADD COLUMN pinned_at timestamptz, ADD COLUMN edited_at timestamptz;

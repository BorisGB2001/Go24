-- «Диск»: who has the document open in the editor now, as the document server tells (callback status 1).
ALTER TABLE files ADD COLUMN editing uuid[], ADD COLUMN editing_at timestamptz;

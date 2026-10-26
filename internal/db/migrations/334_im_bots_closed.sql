-- A chat bot that is gone keeps its row (GO24DEV-641): its name stays on its messages and chats; closed_at — when
-- go24 worker closed its Matrix account.
ALTER TABLE im_bots ADD COLUMN closed_at timestamptz;

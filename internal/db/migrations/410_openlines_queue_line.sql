-- «Передать в другую линию»: the line whose queue handles the dialogue now (NULL — its own line);
-- the client stays in the channel they write from (the widget or the bot of the dialogue's own line).
ALTER TABLE ol_sessions ADD COLUMN queue_id uuid REFERENCES ol_widgets(id) ON DELETE SET NULL;

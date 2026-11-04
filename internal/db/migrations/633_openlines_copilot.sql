-- Open lines (GO24DEV-818): CoPilot's summary of a dialogue — the AI writes it from the conversation, by
-- «Подвести итоги» or by itself when a dialogue of a line with ai_summary is closed (summary_tried — it was given once).
ALTER TABLE ol_widgets ADD COLUMN ai_summary bool NOT NULL DEFAULT false;
ALTER TABLE ol_sessions ADD COLUMN summary text NOT NULL DEFAULT '',
    ADD COLUMN summary_tried bool NOT NULL DEFAULT false;

-- Events of other sections in the live feed (GO24DEV-468): a new employee («Приветствуем нового
-- сотрудника»), a meeting of the calendar one is invited to, a new page of the knowledge base — each a post with
-- comments and reactions. source says what it is of (welcome | meeting | kb), source_id — which one, source_data —
-- what the post shows of it (the time and place of a meeting, the space of a page).
ALTER TABLE feed_posts ADD COLUMN source text NOT NULL DEFAULT '',
    ADD COLUMN source_id text NOT NULL DEFAULT '',
    ADD COLUMN source_data jsonb NOT NULL DEFAULT '{}';
CREATE UNIQUE INDEX feed_posts_source_idx ON feed_posts (source, source_id) WHERE source <> '';

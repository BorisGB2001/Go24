-- «Интересы» of an employee's profile (GO24DEV-836): tags the person puts on themselves; a tag leads
-- to the colleagues with the same interest. One spelling per person, compared without case.
CREATE TABLE IF NOT EXISTS user_interests (
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    tag     text NOT NULL CHECK (tag <> '' AND length(tag) <= 50),
    PRIMARY KEY (user_id, tag)
);
CREATE UNIQUE INDEX IF NOT EXISTS user_interests_lower ON user_interests (user_id, lower(tag));
CREATE INDEX IF NOT EXISTS user_interests_tag ON user_interests (lower(tag));

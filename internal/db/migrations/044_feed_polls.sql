-- «Опрос» in a feed post: a question with answers, one or several choices; a person may change the vote.
CREATE TABLE feed_polls (
    post_id  uuid PRIMARY KEY REFERENCES feed_posts(id) ON DELETE CASCADE,
    question text NOT NULL CHECK (length(question) BETWEEN 1 AND 300),
    multiple boolean NOT NULL DEFAULT false
);
CREATE TABLE feed_poll_options (
    id      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    post_id uuid NOT NULL REFERENCES feed_polls(post_id) ON DELETE CASCADE,
    text    text NOT NULL CHECK (length(text) BETWEEN 1 AND 200),
    sort    int NOT NULL
);
CREATE INDEX feed_poll_options_post_idx ON feed_poll_options (post_id, sort);
CREATE TABLE feed_poll_votes (
    option_id uuid NOT NULL REFERENCES feed_poll_options(id) ON DELETE CASCADE,
    post_id   uuid NOT NULL REFERENCES feed_polls(post_id) ON DELETE CASCADE,
    user_id   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (option_id, user_id)
);
CREATE INDEX feed_poll_votes_post_idx ON feed_poll_votes (post_id, user_id);

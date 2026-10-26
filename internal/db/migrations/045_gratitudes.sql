-- «Благодарность»: a feed post that thanks colleagues, with a badge; it shows in their profiles too.
CREATE TABLE feed_gratitudes (
    post_id uuid PRIMARY KEY REFERENCES feed_posts(id) ON DELETE CASCADE,
    badge   text NOT NULL CHECK (badge IN ('cup', 'star', 'heart', 'like', 'flowers', 'smile', 'gift', 'crown', 'rocket'))
);
CREATE TABLE feed_gratitude_users (
    post_id uuid NOT NULL REFERENCES feed_gratitudes(post_id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (post_id, user_id)
);
CREATE INDEX feed_gratitude_users_user_idx ON feed_gratitude_users (user_id);

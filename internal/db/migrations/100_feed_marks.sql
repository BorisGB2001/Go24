-- Marks of feed posts a person keeps for themselves, as in the portal live feed (GO24DEV-273): «В избранное» (the
-- tab «Избранное») and «Закрепить» (shown above the feed).
CREATE TABLE feed_favorites (
    post_id uuid NOT NULL REFERENCES feed_posts(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, post_id)
);
CREATE TABLE feed_pins (
    post_id uuid NOT NULL REFERENCES feed_posts(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, post_id)
);

-- «Лента» (the portal live feed): posts to all employees or to a project's members, comments, likes,
-- important posts that people confirm as read. Task events are shown in the feed from task_history.
CREATE TABLE feed_posts (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    author_id  uuid NOT NULL REFERENCES users(id),
    project_id uuid REFERENCES projects(id) ON DELETE CASCADE, -- NULL = all employees
    title      text NOT NULL DEFAULT '',
    body       text NOT NULL CHECK (length(body) BETWEEN 1 AND 20000),
    important  boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX feed_posts_created_idx ON feed_posts (created_at DESC);

CREATE TABLE feed_comments (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    post_id    uuid NOT NULL REFERENCES feed_posts(id) ON DELETE CASCADE,
    author_id  uuid NOT NULL REFERENCES users(id),
    body       text NOT NULL CHECK (length(body) BETWEEN 1 AND 5000),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX feed_comments_post_idx ON feed_comments (post_id, created_at);

CREATE TABLE feed_likes (
    post_id uuid NOT NULL REFERENCES feed_posts(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (post_id, user_id)
);

-- «Прочитано» for important posts
CREATE TABLE feed_reads (
    post_id uuid NOT NULL REFERENCES feed_posts(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    read_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (post_id, user_id)
);

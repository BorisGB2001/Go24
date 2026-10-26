-- Files and pictures in feed posts: a file belongs to a post (deleting the post leaves it to its uploader).
ALTER TABLE files ADD COLUMN post_id uuid REFERENCES feed_posts(id) ON DELETE SET NULL;
CREATE INDEX files_post_idx ON files (post_id) WHERE post_id IS NOT NULL;

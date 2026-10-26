-- Files and pictures in comments of the feed (GO24DEV-463): a comment's file also carries its post
-- (downloaded by the post's audience); it goes away with its comment. Repeatable: the work base got it as
-- 203_feed_comment_files before the merge of «Сайты» dropped that file.
ALTER TABLE files ADD COLUMN IF NOT EXISTS feed_comment_id uuid REFERENCES feed_comments(id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS files_feed_comment_idx ON files (feed_comment_id) WHERE feed_comment_id IS NOT NULL;
-- A comment of files alone has no text.
ALTER TABLE feed_comments DROP CONSTRAINT IF EXISTS feed_comments_body_check,
    ADD CONSTRAINT feed_comments_body_check CHECK (length(body) <= 5000);

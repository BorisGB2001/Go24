-- «изменено» at an edited comment of the feed, as at an edited post
ALTER TABLE feed_comments ADD COLUMN edited_at timestamptz;

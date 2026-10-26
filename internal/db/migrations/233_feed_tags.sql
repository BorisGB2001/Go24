-- Tags of feed posts: the «Теги» of a post and the #tags of its text; a tag opens the feed of the
-- posts with it. Kept lowercase, without "#".
CREATE TABLE feed_tags (
	post_id uuid NOT NULL REFERENCES feed_posts(id) ON DELETE CASCADE,
	tag     text NOT NULL,
	typed   boolean NOT NULL DEFAULT false, -- from the «Теги» field (else a #tag of the text)
	PRIMARY KEY (post_id, tag)
);
CREATE INDEX feed_tags_tag ON feed_tags (tag);

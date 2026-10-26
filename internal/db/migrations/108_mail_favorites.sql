-- «Избранное» of the mail's folder pane, as Outlook's: the user's own shortcuts to folders, categories, saved searches and
-- the virtual folders, in their order. ref is what the page's ?folder= takes: a folder's id, "cat:ID", "search:ID",
-- "flagged", "snoozed", "all".
CREATE TABLE mail_favorites (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    ref     text NOT NULL CHECK (length(ref) BETWEEN 1 AND 100),
    pos     int  NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, ref)
);

-- the defaults (Inbox, Sent, Drafts) are put once: a user who removed them all has none
ALTER TABLE users ADD COLUMN mail_favorites_made boolean NOT NULL DEFAULT false;

-- Calendars of colleagues, as «Календари сотрудников» (GO24DEV-516): a person adds colleagues' calendars
-- to their panel, each in its colour, and sees their meetings over their own. What colleagues see of one's calendar is
-- one's choice: only the busy time ('busy', the default) or the meetings in full ('full'); a private
-- meeting is always only «Занят».
CREATE TABLE calendar_follows (
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    target_id  uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    color      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, target_id)
);
ALTER TABLE users ADD COLUMN calendar_share text NOT NULL DEFAULT 'busy';

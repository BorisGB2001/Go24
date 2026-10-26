-- A/B test of a campaign: variant B (its subject and/or letter; empty — A's) goes with A to a share of
-- the addressees; when the test's hours are over (or the author chooses), the winner goes to the rest, held till then.
ALTER TABLE mkt_campaigns
    ADD COLUMN ab_subject text NOT NULL DEFAULT '',
    ADD COLUMN ab_body    text NOT NULL DEFAULT '',
    ADD COLUMN ab_share   int  NOT NULL DEFAULT 0 CHECK (ab_share = 0 OR ab_share BETWEEN 10 AND 100),
    ADD COLUMN ab_hours   int  NOT NULL DEFAULT 4 CHECK (ab_hours BETWEEN 1 AND 72),
    ADD COLUMN ab_metric  text NOT NULL DEFAULT 'opened' CHECK (ab_metric IN ('opened', 'clicked')),
    ADD COLUMN ab_winner  text NOT NULL DEFAULT '' CHECK (ab_winner IN ('', 'a', 'b'));

ALTER TABLE mkt_recipients ADD COLUMN variant text NOT NULL DEFAULT '' CHECK (variant IN ('', 'a', 'b'));
ALTER TABLE mkt_recipients DROP CONSTRAINT IF EXISTS mkt_recipients_status_check;
ALTER TABLE mkt_recipients ADD CONSTRAINT mkt_recipients_status_check CHECK (status IN ('pending', 'held', 'sent', 'error', 'skipped'));

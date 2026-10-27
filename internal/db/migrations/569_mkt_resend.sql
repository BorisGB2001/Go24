-- «Повторная рассылка»: a draft of a sent campaign's letter to its addressees who did not open it
-- (unopened) or follow its links (unclicked); its addressees are those of resend_of, not of segments.
ALTER TABLE mkt_campaigns
    ADD COLUMN resend_of uuid REFERENCES mkt_campaigns (id) ON DELETE SET NULL,
    ADD COLUMN resend_to text NOT NULL DEFAULT '' CHECK (resend_to IN ('', 'unopened', 'unclicked'));

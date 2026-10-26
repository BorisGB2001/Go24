-- The messenger's «Звонки» (GO24DEV-332): a missed or declined call counts beside the tab until the
-- person has looked at the call history.
ALTER TABLE call_ring_people ADD COLUMN seen boolean NOT NULL DEFAULT false;

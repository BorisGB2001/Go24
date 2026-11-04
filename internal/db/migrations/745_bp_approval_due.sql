-- «Время на выполнение» of approvals, reviews and requests of workflows (GO24DEV-878): by due_at the
-- person has decided, or the step ends without them (the worker looks for the ones over).
ALTER TABLE bp_approvals ADD COLUMN due_at timestamptz;
CREATE INDEX bp_approvals_due_idx ON bp_approvals (due_at) WHERE decision IS NULL AND due_at IS NOT NULL;

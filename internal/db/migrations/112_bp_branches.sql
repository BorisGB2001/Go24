-- Business processes, step 2 (GO24DEV-291): an approval may go to several people and a review («Ознакомление»)
-- waits until everyone has read it — a person's part is a bp_approvals row of the step's kind.
ALTER TABLE bp_approvals ADD COLUMN kind text NOT NULL DEFAULT 'approve' CHECK (kind IN ('approve', 'review'));
ALTER TABLE bp_approvals DROP CONSTRAINT bp_approvals_decision_check;
ALTER TABLE bp_approvals ADD CONSTRAINT bp_approvals_decision_check CHECK (decision IN ('approve', 'reject', 'seen'));

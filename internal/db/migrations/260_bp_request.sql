-- Business processes (GO24DEV-387): «Запрос дополнительной информации» — a person fills in the step's fields (its
-- bp_approvals row of kind request, decision done) or, when the step allows, rejects it.
ALTER TABLE bp_approvals DROP CONSTRAINT bp_approvals_kind_check;
ALTER TABLE bp_approvals ADD CONSTRAINT bp_approvals_kind_check CHECK (kind IN ('approve', 'review', 'request'));
ALTER TABLE bp_approvals DROP CONSTRAINT bp_approvals_decision_check;
ALTER TABLE bp_approvals ADD CONSTRAINT bp_approvals_decision_check CHECK (decision IN ('approve', 'reject', 'seen', 'done'));

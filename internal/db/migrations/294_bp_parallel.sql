-- «Параллельное выполнение»: a parallel step runs its branches (ranges of the steps after it) at once, each with
-- its own step; the process stays on the parallel step until every branch is over, then goes on where they join.
CREATE TABLE bp_branches (
    instance_id uuid NOT NULL REFERENCES bp_instances(id) ON DELETE CASCADE,
    n           int NOT NULL,                   -- the branch, from 0
    step        int NOT NULL,                   -- the branch's step running or waited on; its end when the branch is over
    waiting     boolean NOT NULL DEFAULT false, -- on an approval, a review, a request or a pause
    wake_at     timestamptz,                    -- a pause ends
    PRIMARY KEY (instance_id, n)
);
CREATE INDEX bp_branches_wake_idx ON bp_branches (wake_at) WHERE wake_at IS NOT NULL;

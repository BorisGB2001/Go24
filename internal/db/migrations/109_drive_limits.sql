-- «Диск»: how much each drive may hold (0 — no limit): every «Мой диск», the company's, every project's
CREATE TABLE drive_limits (
    kind text PRIMARY KEY CHECK (kind IN ('user', 'company', 'project')),
    bytes bigint NOT NULL CHECK (bytes >= 0)
);

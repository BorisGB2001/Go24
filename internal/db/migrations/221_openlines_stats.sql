-- «Статистика» of the open lines: when a dialogue started (the visitor's first message) and when it was closed.
ALTER TABLE ol_sessions
    ADD COLUMN started_at timestamptz,
    ADD COLUMN closed_at  timestamptz;

UPDATE ol_sessions s SET started_at = (SELECT min(m.at) FROM ol_messages m WHERE m.session_id = s.id AND m.author_id IS NULL AND m.kind = '');
UPDATE ol_sessions SET closed_at = last_at WHERE status = 'closed';

CREATE INDEX ol_sessions_started ON ol_sessions (started_at) WHERE started_at IS NOT NULL;
CREATE INDEX ol_sessions_closed ON ol_sessions (closed_at) WHERE closed_at IS NOT NULL;

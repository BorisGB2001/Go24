-- «Оценка» of a task (the portal MARK: the creator rates finished work) and «Результат» (a chat message marked as the
-- task's result, shown apart in the card).
ALTER TABLE tasks ADD COLUMN rating text NOT NULL DEFAULT '' CHECK (rating IN ('', 'positive', 'negative'));

CREATE TABLE task_results (
    task_id    uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    comment_id uuid NOT NULL REFERENCES task_comments(id) ON DELETE CASCADE,
    created_by uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, comment_id)
);

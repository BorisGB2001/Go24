-- «Просмотры» of a task: who opened its card, the first and the last time, and how often.
CREATE TABLE task_views (
	task_id   uuid NOT NULL REFERENCES tasks_all(id) ON DELETE CASCADE,
	user_id   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	first_at  timestamptz NOT NULL DEFAULT now(),
	viewed_at timestamptz NOT NULL DEFAULT now(),
	times     int NOT NULL DEFAULT 1,
	PRIMARY KEY (task_id, user_id)
);

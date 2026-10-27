-- «Обучение», tests (GO24DEV-790): an item of a course's plan is a lesson or a test; a test has questions (one answer,
-- several answers, a typed answer) and is passed by an attempt scoring at least its pass percentage — then it counts
-- as passed like a lesson. Attempts may be limited in number and in time; the questions may be shuffled.
ALTER TABLE learn_lessons
    ADD COLUMN kind     text NOT NULL DEFAULT 'lesson' CHECK (kind IN ('lesson', 'test')),
    ADD COLUMN pass_pct int NOT NULL DEFAULT 70 CHECK (pass_pct BETWEEN 1 AND 100),
    ADD COLUMN attempts int NOT NULL DEFAULT 0 CHECK (attempts >= 0), -- 0 — any number
    ADD COLUMN minutes  int NOT NULL DEFAULT 0 CHECK (minutes >= 0),  -- 0 — no time limit
    ADD COLUMN shuffle  boolean NOT NULL DEFAULT false;

CREATE TABLE learn_questions (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    lesson_id uuid NOT NULL REFERENCES learn_lessons(id) ON DELETE CASCADE,
    kind      text NOT NULL CHECK (kind IN ('one', 'many', 'text')),
    text      text NOT NULL CHECK (length(text) BETWEEN 1 AND 5000),
    options   jsonb NOT NULL DEFAULT '[]', -- [{"text", "right"}] of «one» and «many»
    answers   text[] NOT NULL DEFAULT '{}', -- the accepted answers of «text» (case and spaces do not matter)
    points    int NOT NULL DEFAULT 1 CHECK (points BETWEEN 1 AND 100),
    sort      int NOT NULL DEFAULT 0
);
CREATE INDEX learn_questions_lesson ON learn_questions (lesson_id, sort);

CREATE TABLE learn_attempts (
    id          bigserial PRIMARY KEY,
    lesson_id   uuid NOT NULL REFERENCES learn_lessons(id) ON DELETE CASCADE,
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    questions   text[] NOT NULL,                -- the ids of the questions in the order shown
    answers     jsonb NOT NULL DEFAULT '{}',    -- {question id: [option numbers] | "typed text"}
    results     jsonb NOT NULL DEFAULT '{}',    -- {question id: true | false}
    score       int NOT NULL DEFAULT 0,
    max_score   int NOT NULL DEFAULT 0,
    passed      boolean NOT NULL DEFAULT false,
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);
CREATE INDEX learn_attempts_user ON learn_attempts (lesson_id, user_id, started_at DESC);

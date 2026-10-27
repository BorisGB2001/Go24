-- «Обучение» learning module (GO24DEV-789): courses of lessons (in chapters), «Кто видит» — people and
-- departments (with their sub-departments), none — everybody; a course is assigned to employees with a due day, each
-- marks the lessons passed. Kept by administrators and HR officers with the right «learning».
CREATE TABLE learn_courses (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title       text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    description text NOT NULL DEFAULT '',
    color       text NOT NULL DEFAULT '',
    published   boolean NOT NULL DEFAULT false,
    created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    changed_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE learn_lessons (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    course_id uuid NOT NULL REFERENCES learn_courses(id) ON DELETE CASCADE,
    chapter   text NOT NULL DEFAULT '', -- the lessons of a chapter go one after another
    title     text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    body      text NOT NULL DEFAULT '', -- Markdown
    sort      int NOT NULL DEFAULT 0
);
CREATE INDEX learn_lessons_course ON learn_lessons (course_id, sort);

CREATE TABLE learn_access (
    course_id     uuid NOT NULL REFERENCES learn_courses(id) ON DELETE CASCADE,
    user_id       uuid REFERENCES users(id) ON DELETE CASCADE,
    department_id uuid REFERENCES departments(id) ON DELETE CASCADE,
    CHECK ((user_id IS NULL) <> (department_id IS NULL))
);
CREATE UNIQUE INDEX learn_access_u ON learn_access (course_id, coalesce(user_id, department_id));

CREATE TABLE learn_assignments (
    course_id   uuid NOT NULL REFERENCES learn_courses(id) ON DELETE CASCADE,
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    assigned_by uuid REFERENCES users(id) ON DELETE SET NULL,
    due_on      date,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (course_id, user_id)
);
CREATE INDEX learn_assignments_user ON learn_assignments (user_id);

CREATE TABLE learn_progress (
    lesson_id uuid NOT NULL REFERENCES learn_lessons(id) ON DELETE CASCADE,
    user_id   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    done_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (lesson_id, user_id)
);
CREATE INDEX learn_progress_user ON learn_progress (user_id);

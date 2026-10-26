-- «Свои наборы данных» of «BI-аналитика»: a table loaded from a CSV file becomes a data set (file_ID) the
-- widgets and the external BI read. columns: [{code, name, header, type}] — code c1, c2… stays when the file is loaded
-- again (the header of the file finds the column), name is shown, type is string | number | date | bool.
CREATE TABLE bi_files (
    id          bigserial PRIMARY KEY,
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    columns     jsonb NOT NULL DEFAULT '[]',
    file_name   text NOT NULL DEFAULT '',
    rows        int NOT NULL DEFAULT 0,
    created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- A row of the file: raw — the cells as in the file by the columns' codes, data — their values of the columns'
-- types (a value not of its type is left out); data is made again from raw when a type changes.
CREATE TABLE bi_file_rows (
    file_id bigint NOT NULL REFERENCES bi_files(id) ON DELETE CASCADE,
    n       int NOT NULL,
    raw     jsonb NOT NULL,
    data    jsonb NOT NULL,
    PRIMARY KEY (file_id, n)
);

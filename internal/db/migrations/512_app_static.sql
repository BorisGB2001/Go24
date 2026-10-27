-- A static application, as «Локальное приложение» made from an archive: no server of its own, its files
-- (index.html, scripts, styles, pictures) are kept here and go24 gives them at /apps/static/<app>/<path>, in a sandbox.
ALTER TABLE apps ADD COLUMN static boolean NOT NULL DEFAULT false;

CREATE TABLE app_files (
    app_id uuid NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    path   text NOT NULL,
    ctype  text NOT NULL,
    data   bytea NOT NULL,
    PRIMARY KEY (app_id, path)
);

-- Mail: «Условное форматирование», as Outlook's — the user's rules that set off letters of the list by a colour and
-- a font: conditions (cond, as mail.Cond — all of them), «Не прочитано» (unread), the look (look, as mail.Look); the
-- first rule on (by pos) that takes a letter decides.
CREATE TABLE mail_formats (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    pos        int NOT NULL DEFAULT 0,
    enabled    boolean NOT NULL DEFAULT true,
    cond       jsonb NOT NULL DEFAULT '{}',
    unread     boolean NOT NULL DEFAULT false,
    look       jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mail_formats_user_idx ON mail_formats (user_id, pos);

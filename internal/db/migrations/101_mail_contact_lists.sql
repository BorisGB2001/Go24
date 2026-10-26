-- Mail as in Outlook: «Списки контактов» — the user's named lists of addresses; typed in «Кому», a list is offered by
-- its name and turns into its people.
CREATE TABLE mail_contact_lists (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    members    jsonb NOT NULL DEFAULT '[]',  -- [{"name": "Анна", "email": "anna@example.com"}, …]
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mail_contact_lists_user_idx ON mail_contact_lists (user_id, lower(name));

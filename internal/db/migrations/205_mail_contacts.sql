-- Mail as in Outlook: «Личные контакты» — the user's own address book (GO24DEV-514): offered in «Кому», «Копия» and
-- «Скрытая» beside go24's employees and the CRM, kept on its tab of the address book.
CREATE TABLE mail_contacts (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       text NOT NULL DEFAULT '' CHECK (length(name) <= 200),
    email      text NOT NULL CHECK (length(email) BETWEEN 3 AND 320),
    company    text NOT NULL DEFAULT '' CHECK (length(company) <= 200),
    phone      text NOT NULL DEFAULT '' CHECK (length(phone) <= 50),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX mail_contacts_user_email_idx ON mail_contacts (user_id, lower(email));

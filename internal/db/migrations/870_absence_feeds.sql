-- «График отсутствий» in Outlook and on a phone «Подключить к Outlook» (GO24DEV-944): the secret link
-- of a person's subscription to the absences of the company.
CREATE TABLE absence_feeds (
    user_id    uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    token      text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

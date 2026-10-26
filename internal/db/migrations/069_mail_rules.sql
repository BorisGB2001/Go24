-- Mail as in Outlook, step 5: rules, automatic replies («Нет на месте»), «Очистка».

-- A rule of the user: conditions (cond: all of them), exceptions (exc: any of them), actions (act) — as JSON, see
-- mail.Rule; run in order (pos) on the new letters of the inbox, or on a folder at once («Запустить сейчас»).
CREATE TABLE mail_rules (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    pos        int NOT NULL DEFAULT 0,
    enabled    boolean NOT NULL DEFAULT true,
    stop       boolean NOT NULL DEFAULT false, -- «Прекратить обработку других правил»
    cond       jsonb NOT NULL DEFAULT '{}',
    exc        jsonb NOT NULL DEFAULT '{}',
    act        jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mail_rules_user_idx ON mail_rules (user_id, pos);

-- Automatic replies: off, on, or for a period; a text for go24's people and the user's own domain, one for the
-- others (none, the people the user wrote to, everyone); on by itself while the user is absent (absences), unless
-- turned off from the banner for that absence (ooo_dismissed: its last day). ooo_since: replies go out once per
-- sender since then.
ALTER TABLE mail_settings
    ADD COLUMN ooo_mode      text NOT NULL DEFAULT 'off' CHECK (ooo_mode IN ('off', 'on', 'period')),
    ADD COLUMN ooo_from      timestamptz,
    ADD COLUMN ooo_to        timestamptz,
    ADD COLUMN ooo_internal  text NOT NULL DEFAULT '' CHECK (length(ooo_internal) <= 10000),
    ADD COLUMN ooo_external  text NOT NULL DEFAULT '' CHECK (length(ooo_external) <= 10000),
    ADD COLUMN ooo_outside   text NOT NULL DEFAULT 'all' CHECK (ooo_outside IN ('none', 'contacts', 'all')),
    ADD COLUMN ooo_absence   boolean NOT NULL DEFAULT false,  -- off until the user turns it on: no automatic letters to outside senders by surprise
    ADD COLUMN ooo_since     timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN ooo_dismissed date;

-- The senders answered in a period of automatic replies (period: 'm:<since>' or 'a:<absence id>').
CREATE TABLE mail_ooo_sent (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    sender  text NOT NULL,
    period  text NOT NULL,
    sent_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, sender, period)
);

-- «Очистка» that stays: a sender's letters of a folder go (to: 'delete', 'archive' or a folder's id) — all of them
-- (future ones too), all but the latest, or those older than days.
CREATE TABLE mail_sweeps (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    folder_id  uuid NOT NULL REFERENCES mail_folders(id) ON DELETE CASCADE,
    sender     text NOT NULL,
    mode       text NOT NULL CHECK (mode IN ('all', 'latest', 'older')),
    days       int NOT NULL DEFAULT 10 CHECK (days BETWEEN 1 AND 3650),
    target     text NOT NULL DEFAULT 'delete',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (folder_id, sender)
);

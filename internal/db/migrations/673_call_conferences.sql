-- «Видеоконференции» (GO24DEV-851): a conference of its own with a title, its people and a link that
-- lasts until the conference is deleted — an employee joins by it at once, a guest with a name (and the password);
-- in «Трансляция» only the presenters speak and show, the others watch and write in the call's chat. link_id is the
-- conference's guest link (call_query 'conf=<id>'), chat — the group chat of its people in the messenger.
CREATE TABLE call_conferences (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title      text NOT NULL,
    created_by uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    members    uuid[] NOT NULL DEFAULT '{}',
    broadcast  boolean NOT NULL DEFAULT false,
    presenters uuid[] NOT NULL DEFAULT '{}',
    link_id    uuid REFERENCES call_guest_links(id) ON DELETE SET NULL,
    chat       text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX call_conferences_by ON call_conferences (created_by);
CREATE INDEX call_conferences_members ON call_conferences USING gin (members);

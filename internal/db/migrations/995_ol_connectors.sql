-- «Открытые линии»: the channels of applications, as imconnector (GO24DEV-989) — an application registers
-- its own way into the lines (a messenger go24 does not know), turns it on for a line and passes the clients' messages
-- (imconnector.send.messages); the operators' answers go to it as the event ONIMCONNECTORMESSAGEADD.
-- code — the connector's ID as the application names it (one for the portal; icon — its picture (a
-- data: URL or an SVG), placement — the application's page that sets it up for a line.
CREATE TABLE ol_connectors (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    app_id     uuid NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    code       text NOT NULL UNIQUE CHECK (code ~ '^[a-z0-9_.-]{1,50}$'),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    icon       text NOT NULL DEFAULT '' CHECK (length(icon) <= 200000),
    placement  text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

-- a connector on a line: active — turned on (imconnector.activate); data — what the application tells about the
-- account it connected (imconnector.connector.data.set: id, url, url_im, name); base — go24's address at the time,
-- the files of the answers are links to it.
CREATE TABLE ol_connector_lines (
    connector_id uuid NOT NULL REFERENCES ol_connectors(id) ON DELETE CASCADE,
    widget_id    uuid NOT NULL REFERENCES ol_widgets(id) ON DELETE CASCADE,
    active       boolean NOT NULL DEFAULT true,
    data         jsonb NOT NULL DEFAULT '{}',
    base         text NOT NULL DEFAULT '',
    error        text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (connector_id, widget_id)
);

-- a dialogue from an application's channel: the connector, the client's chat and user there (as the application
-- names them), the last message passed to the application, when its closing was told to the application.
ALTER TABLE ol_sessions DROP CONSTRAINT ol_sessions_channel_check,
    ADD CONSTRAINT ol_sessions_channel_check CHECK (channel IN ('widget', 'telegram', 'max', 'vk', 'viber', 'ok', 'avito', 'whatsapp', 'app')),
    ADD COLUMN connector_id uuid REFERENCES ol_connectors(id) ON DELETE SET NULL,
    ADD COLUMN app_chat text NOT NULL DEFAULT '',
    ADD COLUMN app_user text NOT NULL DEFAULT '',
    ADD COLUMN app_sent bigint NOT NULL DEFAULT 0,
    ADD COLUMN app_finished timestamptz;
CREATE INDEX ol_sessions_app_idx ON ol_sessions (connector_id) WHERE channel = 'app';

-- a client's message from an application: its id there (imconnector.update.messages and delete.messages find it).
ALTER TABLE ol_messages ADD COLUMN app_id text;
CREATE INDEX ol_messages_app_idx ON ol_messages (session_id, app_id) WHERE app_id IS NOT NULL;

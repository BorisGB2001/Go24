-- SMS to clients: the providers of «Центр сообщений» (SMS.ru, SMSC, Twilio, an own gateway) and the
-- messages sent through them; a message sent from a CRM card is also a line «sms» of its timeline (stream crm).
CREATE TABLE sms_senders (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind       text NOT NULL CHECK (kind IN ('smsru', 'smsc', 'twilio', 'gateway')),
    name       text NOT NULL,
    login      text NOT NULL DEFAULT '',  -- SMSC login, Twilio account SID, the gateway's address
    secret     bytea,                     -- the API key, password or token, sealed
    sender     text NOT NULL DEFAULT '',  -- the sender's name or number the client sees
    is_default boolean NOT NULL DEFAULT false,
    active     boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX sms_senders_default_idx ON sms_senders (is_default) WHERE is_default;

CREATE TABLE sms_messages (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    sender_id   uuid REFERENCES sms_senders(id) ON DELETE SET NULL,
    phone       text NOT NULL,
    body        text NOT NULL,
    status      text NOT NULL CHECK (status IN ('sent', 'error')),
    error       text NOT NULL DEFAULT '',
    external_id text NOT NULL DEFAULT '',
    author_id   uuid REFERENCES users(id) ON DELETE SET NULL,
    entity      text NOT NULL DEFAULT '',
    entity_id   uuid,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sms_messages_entity_idx ON sms_messages (entity, entity_id, created_at);

ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check
    CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot', 'document', 'mail', 'activity', 'invoice', 'merged', 'recurred',
        'recurred_from', 'changed', 'tunnel_to', 'tunnel_from', 'pipeline', 'openline', 'call', 'mailing', 'restored', 'booking',
        'ship_auto', 'ship_draft', 'repeat', 'bound', 'unbound', 'doc_viewed', 'doc_signed', 'link_clicked', 'copilot_fill', 'sms'));

-- the robot «Отправить SMS клиенту»; sign («Отправить на подпись») was missing here though the robots offer it
ALTER TABLE crm_robots DROP CONSTRAINT crm_robots_kind_check;
ALTER TABLE crm_robots ADD CONSTRAINT crm_robots_kind_check
    CHECK (kind IN ('task', 'notify', 'comment', 'activity', 'mail', 'responsible', 'stage', 'copy', 'move', 'booking',
        'fields', 'webhook', 'infocall', 'observers', 'document', 'bp', 'close', 'sign', 'sms'));

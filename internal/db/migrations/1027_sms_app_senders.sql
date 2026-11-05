-- SMS providers of applications, as messageservice.* (GO24DEV-1027): an application registers its
-- provider by a code with a handler on its host; go24 posts every message to the handler and the application tells
-- how it went (messageservice.message.status.update).
ALTER TABLE sms_senders DROP CONSTRAINT sms_senders_kind_check;
ALTER TABLE sms_senders ADD CONSTRAINT sms_senders_kind_check CHECK (kind IN ('smsru', 'smsc', 'twilio', 'gateway', 'app'));
ALTER TABLE sms_senders ADD COLUMN app_id uuid REFERENCES apps(id) ON DELETE CASCADE,
    ADD COLUMN code text NOT NULL DEFAULT '',          -- the application's code of the provider
    ADD COLUMN description text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX sms_senders_app_code_idx ON sms_senders (app_id, code) WHERE app_id IS NOT NULL;

-- delivered and undelivered: what the application says of a message it took
ALTER TABLE sms_messages DROP CONSTRAINT sms_messages_status_check;
ALTER TABLE sms_messages ADD CONSTRAINT sms_messages_status_check CHECK (status IN ('sent', 'error', 'delivered', 'undelivered'));
ALTER TABLE sms_messages ADD COLUMN status_at timestamptz;

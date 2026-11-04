-- Robots of the portal CRM that make cards of a card or ask about it: «Конвертировать лид», «Создать на основании»,
-- «Запросить дополнительную информацию» — its requests below: whom it asks to fill which fields of the card, and the
-- robots of the stage after it, held until the fields are filled.
ALTER TABLE crm_robots DROP CONSTRAINT crm_robots_kind_check;
ALTER TABLE crm_robots ADD CONSTRAINT crm_robots_kind_check
    CHECK (kind IN ('task', 'notify', 'comment', 'activity', 'mail', 'responsible', 'stage', 'copy', 'move', 'booking',
        'fields', 'webhook', 'infocall', 'observers', 'document', 'bp', 'close', 'sign', 'sms', 'request', 'create', 'convert'));

CREATE TABLE crm_info_requests (
    id         bigserial PRIMARY KEY,
    robot_id   uuid REFERENCES crm_robots(id) ON DELETE SET NULL,
    entity     text NOT NULL CHECK (entity IN ('deal', 'lead')),
    entity_id  uuid NOT NULL,
    stage_id   uuid NOT NULL REFERENCES crm_stages(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id),
    actor_id   uuid NOT NULL REFERENCES users(id),
    body       text NOT NULL DEFAULT '',
    fields     text[] NOT NULL,
    held       uuid[] NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    done_at    timestamptz
);
CREATE INDEX crm_info_requests_card_idx ON crm_info_requests (entity, entity_id) WHERE done_at IS NULL;

-- The cards a create robot makes and the card it made them of tell of each other.
ALTER TABLE crm_timeline DROP CONSTRAINT crm_timeline_kind_check;
ALTER TABLE crm_timeline ADD CONSTRAINT crm_timeline_kind_check
    CHECK (kind IN ('comment', 'created', 'stage', 'converted', 'robot', 'document', 'mail', 'activity', 'invoice', 'merged', 'recurred',
        'recurred_from', 'changed', 'tunnel_to', 'tunnel_from', 'pipeline', 'openline', 'call', 'mailing', 'restored', 'booking',
        'ship_auto', 'ship_draft', 'repeat', 'bound', 'unbound', 'doc_viewed', 'doc_signed', 'link_clicked', 'copilot_fill', 'sms',
        'wait', 'wait_done', 'wait_over', 'chat', 'doc_sent', 'based_to', 'based_from'));

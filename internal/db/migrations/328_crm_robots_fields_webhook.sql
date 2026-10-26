-- «Изменить элемент» (sets fields of the card) and «Webhook» (posts the card to an address) robots of the CRM, as in
-- the portal; their settings and any robot's condition live in params.
ALTER TABLE crm_robots DROP CONSTRAINT crm_robots_kind_check;
ALTER TABLE crm_robots ADD CONSTRAINT crm_robots_kind_check
    CHECK (kind IN ('task', 'notify', 'comment', 'activity', 'mail', 'responsible', 'stage', 'copy', 'move', 'booking',
        'fields', 'webhook'));

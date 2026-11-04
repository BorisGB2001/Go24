-- The robot «Отправить SMS» (GO24DEV-856, migration 700) lost its kind to the check of migration 723, which is applied
-- after it: the robot could not be saved. Every kind of crm.RobotKinds.
ALTER TABLE crm_robots DROP CONSTRAINT crm_robots_kind_check;
ALTER TABLE crm_robots ADD CONSTRAINT crm_robots_kind_check
    CHECK (kind IN ('task', 'notify', 'comment', 'activity', 'mail', 'responsible', 'stage', 'copy', 'move', 'booking',
        'fields', 'webhook', 'infocall', 'observers', 'document', 'bp', 'close', 'sign', 'sms'));

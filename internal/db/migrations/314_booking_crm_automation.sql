-- «Онлайн-запись» in the CRM automation: triggers of a booking of the deal (the client booked,
-- confirmed, came, did not come, the booking was cancelled) and the robot that sends the client a booking link.
ALTER TABLE crm_triggers DROP CONSTRAINT crm_triggers_kind_check;
ALTER TABLE crm_triggers ADD CONSTRAINT crm_triggers_kind_check
    CHECK (kind IN ('mail', 'form', 'openline', 'activity', 'invoice_paid', 'quote_accepted',
        'booking', 'booking_confirmed', 'booking_came', 'booking_no_show', 'booking_cancelled'));

ALTER TABLE crm_robots DROP CONSTRAINT crm_robots_kind_check;
ALTER TABLE crm_robots ADD CONSTRAINT crm_robots_kind_check
    CHECK (kind IN ('task', 'notify', 'comment', 'activity', 'mail', 'responsible', 'stage', 'copy', 'move', 'booking'));

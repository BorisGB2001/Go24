-- «Изменение статуса задачи» (a trigger): a task linked to the card came to a status.
ALTER TABLE crm_triggers DROP CONSTRAINT crm_triggers_kind_check;
ALTER TABLE crm_triggers ADD CONSTRAINT crm_triggers_kind_check
    CHECK (kind IN ('mail', 'form', 'openline', 'activity', 'invoice_paid', 'quote_accepted',
        'booking', 'booking_confirmed', 'booking_came', 'booking_no_show', 'booking_cancelled', 'booking_moved',
        'call', 'call_missed', 'field', 'webhook', 'doc_viewed', 'doc_signed', 'task'));

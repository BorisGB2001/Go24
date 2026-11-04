-- More «Триггеры» of the portal CRM: a call of the client, a missed call, fields of the card changed («Изменение
-- полей»), a webhook (an outside service calls crm.automation.trigger with the trigger's code). Any trigger may have a
-- condition and may move the card back («Разрешить переход на предыдущую стадию») — its params.
ALTER TABLE crm_triggers ADD COLUMN params jsonb NOT NULL DEFAULT '{}';
ALTER TABLE crm_triggers DROP CONSTRAINT crm_triggers_kind_check;
ALTER TABLE crm_triggers ADD CONSTRAINT crm_triggers_kind_check
    CHECK (kind IN ('mail', 'form', 'openline', 'activity', 'invoice_paid', 'quote_accepted',
        'booking', 'booking_confirmed', 'booking_came', 'booking_no_show', 'booking_cancelled',
        'call', 'call_missed', 'field', 'webhook'));
CREATE UNIQUE INDEX crm_triggers_code ON crm_triggers ((params->>'code')) WHERE params ? 'code';

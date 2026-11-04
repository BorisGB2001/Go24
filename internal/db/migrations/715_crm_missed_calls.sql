-- «Перезвонить»: a client's missed call plans a call deed on the card; the client's number it waits
-- for — talking with the client closes the deed by itself.
ALTER TABLE crm_activities ADD COLUMN callback_number text;
CREATE INDEX crm_activities_callback ON crm_activities (entity, entity_id) WHERE callback_number IS NOT NULL AND done_at IS NULL;

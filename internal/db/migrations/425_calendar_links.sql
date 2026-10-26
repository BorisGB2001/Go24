-- A calendar connected by its iCal link (GO24DEV-670) connects outside calendars: one of the person's
-- own calendars whose meetings go24-worker loads from source_url (read-only in go24).
ALTER TABLE user_calendars ADD COLUMN source_url text NOT NULL DEFAULT '',
    ADD COLUMN synced_at timestamptz,
    ADD COLUMN sync_error text NOT NULL DEFAULT '';
CREATE INDEX user_calendars_links ON user_calendars (synced_at) WHERE source_url <> '';

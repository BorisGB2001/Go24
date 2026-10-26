-- A calendar of a CalDAV server connected two ways (GO24DEV-698), as «Подключить CalDAV / Яндекс / iCloud» of
-- the portal: one of the person's own calendars that go24-worker keeps the same as the server's collection dav_url —
-- the server's changes come into go24, the changes made in go24 go to the server.
ALTER TABLE user_calendars ADD COLUMN dav_url text NOT NULL DEFAULT '',
    ADD COLUMN dav_login text NOT NULL DEFAULT '',
    ADD COLUMN dav_secret bytea; -- the password, sealed with go24's key (AES-GCM)
CREATE INDEX user_calendars_dav ON user_calendars (synced_at) WHERE dav_url <> '';

-- calendar_dav_items are the resources of the collection as the last sync left them: the server's tag (ETag) and the
-- tag of the meeting in go24 (DavItem.ETag) — a change of either is a change made on that side since; tag '' — a
-- resource go24 does not take (a task, a journal).
CREATE TABLE calendar_dav_items (
    calendar_id uuid NOT NULL REFERENCES user_calendars(id) ON DELETE CASCADE,
    name        text NOT NULL,
    etag        text NOT NULL DEFAULT '',
    tag         text NOT NULL DEFAULT '',
    PRIMARY KEY (calendar_id, name)
);

-- dav_dirty: a meeting of the calendar was changed (in go24 or by the sync itself) since its sync began — go24-worker
-- syncs it again within a minute instead of waiting for its round.
ALTER TABLE user_calendars ADD COLUMN dav_dirty boolean NOT NULL DEFAULT false;
CREATE FUNCTION calendar_dav_dirty() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME = 'events' THEN
        UPDATE user_calendars SET dav_dirty = true
        WHERE dav_url <> '' AND NOT dav_dirty AND id IN (OLD.calendar_id, NEW.calendar_id);
    ELSE
        UPDATE user_calendars c SET dav_dirty = true FROM events e
        WHERE e.id IN (OLD.event_id, NEW.event_id) AND c.id = e.calendar_id AND c.dav_url <> '' AND NOT c.dav_dirty;
    END IF;
    RETURN NULL;
END $$;
CREATE TRIGGER events_dav_dirty AFTER INSERT OR UPDATE OR DELETE ON events
    FOR EACH ROW EXECUTE FUNCTION calendar_dav_dirty();
CREATE TRIGGER event_exceptions_dav_dirty AFTER INSERT OR DELETE ON event_exceptions
    FOR EACH ROW EXECUTE FUNCTION calendar_dav_dirty();

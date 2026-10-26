-- Business processes of the documents of «Общий диск» Диск (GO24DEV-697): a template of files starts by
-- itself on a document added to the drive (uploaded, copied, moved there; not one brought back from the trash) or
-- changed (a new version, one brought back, a save of the editor or of WebDAV). The trigger notes every way a file
-- comes and changes; the worker starts the templates by these events as it does by the CRM timeline.
ALTER TABLE bp_templates DROP CONSTRAINT IF EXISTS bp_templates_entity_check;
ALTER TABLE bp_templates ADD CONSTRAINT bp_templates_entity_check CHECK (entity IN ('none', 'deal', 'lead', 'contact', 'company', 'file'));

CREATE TABLE drive_events (
    id         bigserial PRIMARY KEY,
    file_id    uuid NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    author_id  uuid REFERENCES users(id) ON DELETE SET NULL,
    created    boolean NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE FUNCTION go24_drive_event() RETURNS trigger AS $$
BEGIN
    IF NEW.drive IS DISTINCT FROM 'company' OR NEW.deleted_at IS NOT NULL THEN
        RETURN NULL;
    END IF;
    IF TG_OP = 'INSERT' OR OLD.drive IS DISTINCT FROM 'company' THEN
        INSERT INTO drive_events (file_id, author_id, created) VALUES (NEW.id, coalesce(NEW.changed_by, NEW.uploaded_by), true);
    -- a board saves itself every few seconds: its saves are not changes a process starts by
    ELSIF OLD.deleted_at IS NULL AND NEW.object_key IS DISTINCT FROM OLD.object_key AND NEW.name NOT LIKE '%.board' THEN
        INSERT INTO drive_events (file_id, author_id, created) VALUES (NEW.id, coalesce(NEW.changed_by, NEW.uploaded_by), false);
    END IF;
    RETURN NULL;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER files_drive_event AFTER INSERT OR UPDATE OF drive, deleted_at, object_key ON files
    FOR EACH ROW EXECUTE FUNCTION go24_drive_event();

-- the worker reads the drive's events after this one
ALTER TABLE bp_cursor ADD COLUMN file_id bigint NOT NULL DEFAULT 0;

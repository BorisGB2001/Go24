-- «Места встраивания» of local applications, as placement.bind (GO24DEV-598): an application puts its
-- handler into a card of go24 (a tab of a CRM card or a task) under its own title; one handler is bound to a place
-- once, the application's uninstall and deletion take its places off.
CREATE TABLE app_placements (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    app_id      uuid NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    placement   text NOT NULL,
    handler_url text NOT NULL CHECK (handler_url ~ '^https?://'),
    title       text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (app_id, placement, handler_url)
);
CREATE INDEX app_placements_place ON app_placements (placement);

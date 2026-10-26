-- What a portal's objects became in go24 (groups → projects, tasks, comments, checklist items), so an import
-- through REST can be repeated without duplicates and can run for several portals (ids repeat between portals).
CREATE TABLE portal_map (
    portal text NOT NULL,   -- short tag, e.g. acme
    kind   text NOT NULL,   -- group | task | comment | check
    portal_id bigint NOT NULL,
    id     uuid NOT NULL,
    PRIMARY KEY (portal, kind, portal_id)
);

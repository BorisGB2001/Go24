-- «Разрешить редактирование» of a public link to a document: those who open it edit the document in ONLYOFFICE
-- without signing in, and what they save is a new version by the link's maker — as the maker opened the drive
-- (opened_as: my, company, a project's key, sh:… or sf:…), so that the maker's rights decide.
ALTER TABLE drive_links ADD COLUMN edit boolean NOT NULL DEFAULT false,
    ADD COLUMN opened_as text NOT NULL DEFAULT '';

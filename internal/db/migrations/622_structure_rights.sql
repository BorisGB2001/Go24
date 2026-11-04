-- «Права доступа» of the company structure (GO24DEV-827): what the heads of departments, their
-- deputies and every employee may do in the structure — in their own departments with the sub-departments (own) or
-- everywhere (all); no row — nothing. Administrators may do everything. The defaults keep what was before: the
-- structure is changed by administrators, a department's chats — also by its head and deputies.
CREATE TABLE structure_rights (
    role   text NOT NULL CHECK (role IN ('head', 'deputy', 'employee')),
    action text NOT NULL,
    scope  text NOT NULL CHECK (scope IN ('own', 'all')),
    PRIMARY KEY (role, action)
);
INSERT INTO structure_rights (role, action, scope) VALUES ('head', 'chats', 'own'), ('deputy', 'chats', 'own');

-- «Права доступа» of working time (GO24DEV-900): what the heads of departments, their deputies and
-- every employee may do in working time — see it (view), change it (edit: days, approvals, report marks, shifts) in
-- their own departments with the sub-departments (own) or everywhere (all), and the settings with the work schedules
-- (settings, all only); no row — nothing. Administrators may do everything. The defaults keep what was before: heads
-- and deputies see and change the working time of their departments.
CREATE TABLE worktime_rights (
    role   text NOT NULL CHECK (role IN ('head', 'deputy', 'employee')),
    action text NOT NULL,
    scope  text NOT NULL CHECK (scope IN ('own', 'all')),
    PRIMARY KEY (role, action)
);
INSERT INTO worktime_rights (role, action, scope) VALUES
    ('head', 'view', 'own'), ('head', 'edit', 'own'), ('deputy', 'view', 'own'), ('deputy', 'edit', 'own');

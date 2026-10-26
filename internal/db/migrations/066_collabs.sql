-- «Коллабы»: a space for work with people outside the company (clients, contractors, freelancers) —
-- its own chat, tasks, drive and calendar. A collab is a project of kind 'collab': owner, moderators (manager),
-- members; its key is made up by go24 (CL1, CL2…), the people do not type it.
ALTER TABLE projects DROP CONSTRAINT projects_kind_check;
ALTER TABLE projects ADD CONSTRAINT projects_kind_check CHECK (kind IN ('group', 'project', 'scrum', 'collab'));
CREATE SEQUENCE collab_key_seq;

-- Guests: people from outside (invited into collabs) — they see only the collabs they are in. A guest is a member
-- of a collab like anyone else and is marked «гость» in its member list and its chat.
ALTER TABLE users ADD COLUMN guest boolean NOT NULL DEFAULT false;

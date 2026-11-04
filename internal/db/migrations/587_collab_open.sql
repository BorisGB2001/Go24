-- «Тип коллаба», as a portal project: an open collab is entered by any employee who has its link (or finds it in the
-- search of «Коллабы») — «Вступить»; a closed one (the default, as before) shows nothing until one is added.
ALTER TABLE projects ADD COLUMN is_open boolean NOT NULL DEFAULT false;

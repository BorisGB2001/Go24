-- «День рождения»: the year of birth is shown only when the employee chose so (GO24DEV-494).
ALTER TABLE users ADD COLUMN birthday_show_year boolean NOT NULL DEFAULT false;

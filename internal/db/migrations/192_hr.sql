-- «Сотрудник кадров» (GO24DEV-492, 495): an administrator makes an employee an HR officer and lets them enter and
-- change anyone's absences (absences) and invite employees (invite).
ALTER TABLE users ADD COLUMN hr boolean NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN hr_rights text[] NOT NULL DEFAULT '{}';
-- the year the employee said «в этом году не напоминать» of their vacation plan
ALTER TABLE users ADD COLUMN vacation_remind_off integer NOT NULL DEFAULT 0;

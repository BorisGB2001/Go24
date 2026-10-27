-- «Обучение» (GO24DEV-791): the reminders of an assignment — the day before its due day to the employee, and once it is
-- overdue to the employee and who assigned it; each once (a new due day sends them again).
ALTER TABLE learn_assignments
    ADD COLUMN due_reminded timestamptz,
    ADD COLUMN overdue_sent timestamptz;

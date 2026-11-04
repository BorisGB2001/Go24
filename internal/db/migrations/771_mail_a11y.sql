-- Mail as in Outlook: «Проверка читаемости» (Accessibility Checker) of a letter being written. «Параметры → Создание
-- и ответ → Проверять читаемость перед отправкой»: a letter with errors (a picture without alternative text, a table
-- without a header row) is sent only after a question (off by default, as in Outlook).
ALTER TABLE mail_settings ADD COLUMN a11y_check bool NOT NULL DEFAULT false;

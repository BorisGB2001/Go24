-- The ready dashboards of «BI-аналитика», as the system ones («Продажи», «Лиды и источники», «Задачи и
-- эффективность», «Рабочее время»): code names one, its title comes from the locale; go24 keeps their widgets as of
-- version and nobody changes them — they are copied into one's own.
ALTER TABLE bi_dashboards ADD COLUMN code text UNIQUE, ADD COLUMN version int NOT NULL DEFAULT 0;

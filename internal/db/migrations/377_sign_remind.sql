-- «Подпись»: автоматические напоминания подписанту (GO24DEV-639) — пока подписант не подписал, go24
-- раз в remind_days дней (0 — не напоминать) напоминает ему: сотруднику уведомлением, клиенту письмом со ссылкой.
ALTER TABLE sign_docs ADD COLUMN remind_days int NOT NULL DEFAULT 1 CHECK (remind_days BETWEEN 0 AND 30);

-- ready_at — когда пришла очередь подписанта; reminded_at — последнее напоминание (и «Отправить ещё раз»);
-- reminders — сколько напоминаний ушло само.
ALTER TABLE sign_parties ADD COLUMN ready_at timestamptz;
ALTER TABLE sign_parties ADD COLUMN reminded_at timestamptz;
ALTER TABLE sign_parties ADD COLUMN reminders int NOT NULL DEFAULT 0;
UPDATE sign_parties SET ready_at = now() WHERE status = 'ready';
CREATE INDEX sign_parties_ready ON sign_parties (doc_id) WHERE status = 'ready';

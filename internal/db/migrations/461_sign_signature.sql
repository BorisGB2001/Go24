-- «Подпись»: собственноручная подпись подписанта (GO24DEV-706) — нарисованная или загруженная картинка
-- (PNG) при подписании, на листе подписания; сотрудник может запомнить свою подпись, она подставится в следующий раз.
ALTER TABLE sign_parties ADD COLUMN signature bytea;
CREATE TABLE sign_my_signatures (
    user_id    uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    image      bytea NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

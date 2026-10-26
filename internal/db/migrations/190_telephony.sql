-- Telephony, as «Телефония» (GO24DEV-501): go24's own PBX (Asterisk). An employee with an inner number
-- (users.inner_phone, given by the administrators) has a SIP account named by it: secret is the password of their
-- SIP phone (shown to them and to the administrators), web_secret — of go24's softphone in the browser (account
-- "w<number>", never shown). Both are sealed with go24's own key. Whether telephony is on is the administrators'
-- setting company_settings 'telephony'.
CREATE TABLE telephony_lines (
    user_id    uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    secret     bytea NOT NULL,
    web_secret bytea NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

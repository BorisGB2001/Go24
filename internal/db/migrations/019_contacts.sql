-- More contacts in the employee card: messengers and a web page (the portal: Skype/Zoom/WWW fields).
ALTER TABLE users
    ADD COLUMN telegram text NOT NULL DEFAULT '', -- @username
    ADD COLUMN whatsapp text NOT NULL DEFAULT '', -- phone number
    ADD COLUMN website  text NOT NULL DEFAULT '';

-- A transferred call (GO24DEV-632): who transferred it (an employee's inner number) and to which number;
-- "" — not transferred. The call's to_user is then the one it was transferred to (who answered last).
ALTER TABLE telephony_calls
    ADD COLUMN transfer_from text NOT NULL DEFAULT '',
    ADD COLUMN transfer_to   text NOT NULL DEFAULT '';

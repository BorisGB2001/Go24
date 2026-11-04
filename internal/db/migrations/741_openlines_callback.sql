-- «Открытые линии»: «Обратный звонок» in the widget (GO24DEV-874) — the line's widget offers the form
-- of telephony's «Обратный звонок»: the visitor leaves a number and go24 calls back as from the form's own address.
ALTER TABLE ol_widgets ADD COLUMN callback_id uuid REFERENCES telephony_callbacks (id) ON DELETE SET NULL;

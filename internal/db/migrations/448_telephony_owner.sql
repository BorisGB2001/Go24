-- «Направлять звонок ответственному»: a call of a client known to CRM (a contact, a company, a lead
-- with the number) rings the card's responsible first, then the line goes on as usual.
ALTER TABLE telephony_trunks ADD COLUMN to_owner boolean NOT NULL DEFAULT false;

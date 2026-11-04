-- «Создавать в CRM» of a portal line (CRM_CREATE, CRM_CREATE_CALL_TYPE): what a call with a number unknown to CRM
-- makes — '' as the company's «Создавать лид по неизвестному номеру», lead, deal (a contact with the phone and a deal)
-- or none — and for which calls: in, out or all.
ALTER TABLE telephony_trunks ADD COLUMN crm_create text NOT NULL DEFAULT '',
	ADD COLUMN crm_create_type text NOT NULL DEFAULT 'in';

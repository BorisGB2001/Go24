-- «Почта» → CRM mailbox settings: letters of the addresses the CRM knows go into their cards by
-- themselves (crm_bind), a letter from an address it does not know makes a lead (crm_leads). Both off at first.
ALTER TABLE mailboxes
    ADD COLUMN crm_bind  boolean NOT NULL DEFAULT false,
    ADD COLUMN crm_leads boolean NOT NULL DEFAULT false;

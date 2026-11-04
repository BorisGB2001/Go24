-- GO24DEV-880: the client signer's phone Sign: the link and the code to sign with come by SMS;
-- code_via — how the last code went (mail | sms), for the signing sheet.
ALTER TABLE sign_parties ADD COLUMN IF NOT EXISTS phone text NOT NULL DEFAULT '';
ALTER TABLE sign_parties ADD COLUMN IF NOT EXISTS code_via text NOT NULL DEFAULT '';

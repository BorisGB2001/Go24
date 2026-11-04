-- GO24DEV-913: «Подпись» with a qualified electronic signature (КЭП): the signer signs the document
-- with their certificate through the CryptoPro browser plug-in; the detached CAdES signature is kept with what its
-- certificate says.
ALTER TABLE sign_parties ADD COLUMN IF NOT EXISTS method text NOT NULL DEFAULT '' CHECK (method IN ('', 'qes'));
ALTER TABLE sign_parties ADD COLUMN IF NOT EXISTS qes_sig bytea;
ALTER TABLE sign_parties ADD COLUMN IF NOT EXISTS qes jsonb;

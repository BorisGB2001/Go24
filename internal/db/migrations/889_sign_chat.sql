-- GO24DEV-945: the client signer gets the link and the code in the open line's chat Sign by
-- messengers: chat — the dialogue (ol_sessions) of the CRM card's client the link goes to ("" — by e-mail or SMS).
ALTER TABLE sign_parties ADD COLUMN IF NOT EXISTS chat text NOT NULL DEFAULT '';

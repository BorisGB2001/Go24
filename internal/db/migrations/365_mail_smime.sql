-- S/MIME of the mail, as Outlook's «Защита электронной почты» (mail/smime.go): the user's own certificates with
-- their keys (sealed with the mail key), the certificates of others learnt from their signed letters, what a letter's
-- signature and encryption were, a draft's «Подписать» and «Зашифровать», the user's defaults for new letters.
CREATE TABLE mail_smime_certs (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email      text NOT NULL,          -- lower case: the address it signs for
    subject    text NOT NULL DEFAULT '',
    issuer     text NOT NULL DEFAULT '',
    not_after  timestamptz NOT NULL,
    sha1       text NOT NULL,          -- the thumbprint, as Windows shows it
    cert       bytea NOT NULL,         -- DER
    chain      bytea[] NOT NULL DEFAULT '{}',
    key        bytea NOT NULL,         -- PKCS #8, sealed
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, sha1)
);
CREATE INDEX mail_smime_certs_email ON mail_smime_certs (email);

CREATE TABLE mail_smime_peers (
    user_id   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email     text NOT NULL,
    cert      bytea NOT NULL,
    not_after timestamptz NOT NULL,
    seen_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, email)
);

ALTER TABLE mail_messages
    ADD COLUMN smime_sign   text NOT NULL DEFAULT '',  -- '', ok, untrusted, other (not the sender's), bad (changed), error
    ADD COLUMN smime_signer text NOT NULL DEFAULT '',  -- the signer: name and address of the certificate
    ADD COLUMN smime_enc    text NOT NULL DEFAULT '';  -- '', yes (decrypted), nokey (not to the user's certificates)
ALTER TABLE mail_drafts
    ADD COLUMN sign    boolean NOT NULL DEFAULT false,
    ADD COLUMN encrypt boolean NOT NULL DEFAULT false;
ALTER TABLE mail_settings
    ADD COLUMN smime_sign    boolean NOT NULL DEFAULT false,
    ADD COLUMN smime_encrypt boolean NOT NULL DEFAULT false;

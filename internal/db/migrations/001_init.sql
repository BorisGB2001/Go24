-- Users come from Authentik (OIDC); go24 keeps a local copy keyed by the OIDC subject.
CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    oidc_sub      text NOT NULL UNIQUE,
    email         text NOT NULL DEFAULT '',
    name          text NOT NULL DEFAULT '',
    groups        text[] NOT NULL DEFAULT '{}',
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE companies (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title      text NOT NULL,
    inn        text NOT NULL DEFAULT '',
    phone      text NOT NULL DEFAULT '',
    email      text NOT NULL DEFAULT '',
    comment    text NOT NULL DEFAULT '',
    legacy_id  integer UNIQUE,          -- b_crm_company.ID, filled by the migrator
    owner_id   uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE contacts (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id  uuid REFERENCES companies(id) ON DELETE SET NULL,
    last_name   text NOT NULL DEFAULT '',
    first_name  text NOT NULL DEFAULT '',
    middle_name text NOT NULL DEFAULT '',
    post        text NOT NULL DEFAULT '',
    phone       text NOT NULL DEFAULT '',
    email       text NOT NULL DEFAULT '',
    comment     text NOT NULL DEFAULT '',
    legacy_id   integer UNIQUE,         -- b_crm_contact.ID
    owner_id    uuid REFERENCES users(id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX contacts_company_idx ON contacts (company_id);
CREATE INDEX contacts_email_idx ON contacts (lower(email));

-- File metadata; the bytes live in the cloud behind rclone (S3 API).
CREATE TABLE files (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    object_key   text NOT NULL UNIQUE,
    name         text NOT NULL,
    size         bigint NOT NULL,
    content_type text NOT NULL DEFAULT 'application/octet-stream',
    uploaded_by  uuid REFERENCES users(id),
    created_at   timestamptz NOT NULL DEFAULT now()
);

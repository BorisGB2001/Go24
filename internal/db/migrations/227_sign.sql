-- «Подпись» (GO24DEV-360): документ отправляется на подпись сотруднику компании и клиенту по очереди.
-- Документ на подписи — своя копия файла (DOCX), чтобы его не меняли пересоздание и удаление документа карточки;
-- подписанный — PDF (или DOCX без сервера документов) с листом подписания в конце.
CREATE TABLE sign_docs (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title          text NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
    entity         text CHECK (entity IN ('deal', 'lead', 'contact', 'company')), -- the CRM card it was sent from
    entity_id      uuid,
    crm_doc_id     uuid REFERENCES crm_documents(id) ON DELETE SET NULL,
    file_id        uuid NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    hash           text NOT NULL,                                                  -- SHA-256 of the file
    pdf_file_id    uuid REFERENCES files(id) ON DELETE SET NULL,                    -- the file in PDF, to read it
    result_file_id uuid REFERENCES files(id) ON DELETE SET NULL,                    -- signed: with the signing sheet
    status         text NOT NULL DEFAULT 'signing' CHECK (status IN ('signing', 'done', 'refused', 'cancelled')),
    created_by     uuid NOT NULL REFERENCES users(id),
    created_at     timestamptz NOT NULL DEFAULT now(),
    done_at        timestamptz
);
CREATE INDEX sign_docs_author ON sign_docs (created_by, created_at DESC);
CREATE INDEX sign_docs_card ON sign_docs (entity, entity_id);

-- Подписанты по порядку: ord 1 — сотрудник компании (подписывает в go24), дальше — клиент по ссылке с кодом из письма.
CREATE TABLE sign_parties (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    doc_id     uuid NOT NULL REFERENCES sign_docs(id) ON DELETE CASCADE,
    ord        int NOT NULL,
    user_id    uuid REFERENCES users(id),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    email      text NOT NULL DEFAULT '',
    token      text UNIQUE,                                                         -- the client's link /sign/s/{token}
    status     text NOT NULL DEFAULT 'wait' CHECK (status IN ('wait', 'ready', 'signed', 'refused')),
    code_hash  text NOT NULL DEFAULT '',
    code_until timestamptz,
    code_tries int NOT NULL DEFAULT 0,
    signed_at  timestamptz,
    ip         text NOT NULL DEFAULT '',
    user_agent text NOT NULL DEFAULT '',
    reason     text NOT NULL DEFAULT '',                                            -- why refused
    UNIQUE (doc_id, ord)
);
CREATE INDEX sign_parties_user ON sign_parties (user_id) WHERE user_id IS NOT NULL;

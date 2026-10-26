-- «Подпись»: согласующие и наблюдатели (GO24DEV-715). Согласующий — сотрудник компании, который в свою
-- очередь до подписантов согласовывает документ или отклоняет его, без подписи и кода; наблюдатели видят документ и
-- узнают, что он подписан всеми, отклонён или отозван.
ALTER TABLE sign_parties ADD COLUMN role text NOT NULL DEFAULT '' CHECK (role IN ('', 'reviewer'));

CREATE TABLE sign_observers (
    doc_id  uuid NOT NULL REFERENCES sign_docs(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (doc_id, user_id)
);
CREATE INDEX sign_observers_user ON sign_observers (user_id);

ALTER TABLE sign_templates ADD COLUMN reviewers uuid[] NOT NULL DEFAULT '{}';
ALTER TABLE sign_templates ADD COLUMN observers uuid[] NOT NULL DEFAULT '{}';

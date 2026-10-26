-- «Подпись»: история документа (GO24DEV-685) — создан, отправлен подписанту, просмотрен, запрошен код,
-- подписан, отказ, напоминание, отозван, подписан всеми. party_id — подписант события, actor_id — сотрудник, кто
-- сделал (нет — клиент по ссылке или сам go24); ok = false — письмо не ушло.
CREATE TABLE sign_events (
    id         bigserial PRIMARY KEY,
    doc_id     uuid NOT NULL REFERENCES sign_docs(id) ON DELETE CASCADE,
    party_id   uuid REFERENCES sign_parties(id) ON DELETE CASCADE,
    actor_id   uuid REFERENCES users(id) ON DELETE SET NULL,
    kind       text NOT NULL,
    ok         boolean NOT NULL DEFAULT true,
    ip         text NOT NULL DEFAULT '',
    note       text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sign_events_doc ON sign_events (doc_id, id);
-- просмотр отмечается один раз на подписанта
CREATE UNIQUE INDEX sign_events_viewed ON sign_events (party_id) WHERE kind = 'viewed';

-- документам, отправленным раньше, — события из того, что уже известно
INSERT INTO sign_events (doc_id, actor_id, kind, created_at) SELECT id, created_by, 'created', created_at FROM sign_docs;
INSERT INTO sign_events (doc_id, party_id, actor_id, kind, ip, note, created_at)
    SELECT doc_id, id, user_id, CASE status WHEN 'signed' THEN 'signed' ELSE 'refused' END, ip, reason, signed_at
    FROM sign_parties WHERE status IN ('signed', 'refused') AND signed_at IS NOT NULL;
INSERT INTO sign_events (doc_id, kind, created_at) SELECT id, 'done', done_at FROM sign_docs WHERE status = 'done' AND done_at IS NOT NULL;
INSERT INTO sign_events (doc_id, kind, created_at) SELECT id, 'cancelled', done_at FROM sign_docs WHERE status = 'cancelled' AND done_at IS NOT NULL;

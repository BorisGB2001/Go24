-- «Подпись для сотрудников»: документы от сотрудника (GO24DEV-726) — шаблон «Сотрудник», по которому
-- сотрудник сам создаёт документ (заявление): подписывает первым, затем представитель компании signer_id.
CREATE TABLE sign_self_templates (
    template_id uuid PRIMARY KEY REFERENCES crm_doc_templates(id) ON DELETE CASCADE,
    signer_id   uuid NOT NULL REFERENCES users(id),
    updated_by  uuid REFERENCES users(id),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

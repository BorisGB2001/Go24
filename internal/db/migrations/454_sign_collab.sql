-- «Подпись»: документ на подпись из коллаба (GO24DEV-691) — гость коллаба подписывает как клиент,
-- об отправке и подписании пишется в чат коллаба, подписанный документ ложится на его диск.
ALTER TABLE sign_docs ADD COLUMN project_id uuid REFERENCES projects(id) ON DELETE SET NULL;

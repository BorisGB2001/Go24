-- «Подпись»: на подпись — и документ генератора у счёта или предложения CRM (GO24DEV-668).
ALTER TABLE sign_docs DROP CONSTRAINT sign_docs_entity_check;
ALTER TABLE sign_docs ADD CONSTRAINT sign_docs_entity_check CHECK (entity IN ('deal', 'lead', 'contact', 'company', 'invoice'));

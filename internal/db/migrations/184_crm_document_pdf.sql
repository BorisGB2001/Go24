-- «Генератор документов»: PDF документа (делается ONLYOFFICE при первом запросе и хранится, пока документ
-- не переделан) и публичная ссылка на документ для клиента (без входа; NULL — выключена).
ALTER TABLE crm_documents ADD COLUMN pdf_file_id uuid REFERENCES files(id) ON DELETE SET NULL,
    ADD COLUMN public_token text UNIQUE;

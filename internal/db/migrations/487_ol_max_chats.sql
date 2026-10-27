-- Open lines (GO24DEV-729): the queue's limits — «Максимальное количество диалогов у оператора»
-- (0 — no limit) counted till the operator's answer ('answer') or till the dialogue is closed ('close'): an operator at
-- the limit gets no new dialogues, they wait in the queue for the first one to be free; crm_forward — «Направлять
-- обращение ответственному сотруднику, если клиент опознан в CRM».
ALTER TABLE ol_widgets
    ADD COLUMN max_chats int NOT NULL DEFAULT 0,
    ADD COLUMN max_chats_type text NOT NULL DEFAULT 'answer' CHECK (max_chats_type IN ('answer', 'close')),
    ADD COLUMN crm_forward bool NOT NULL DEFAULT false;

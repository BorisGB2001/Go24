-- Page templates of the knowledge base, as in Confluence and корпоративного портала: a new page is made from a ready text — one of
-- go24's own (in the code) or a page of the company saved as a template («Сохранить как шаблон страницы»). They live
-- with the templates of bases: kind 'page' keeps one page in pages, with copies of its pictures and files.
ALTER TABLE kb_templates ADD COLUMN kind text NOT NULL DEFAULT 'space' CHECK (kind IN ('space', 'page'));

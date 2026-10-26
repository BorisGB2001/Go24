-- «Сайты»: the settings of a site and a page. A site has its header and footer — blocks shown on every
-- page above and below its own ones —, a page shown for a missing address (404), a favicon, the counters (Яндекс
-- Метрика, Google Analytics: their ids only, the code is go24's), the accent colour and the font, its own domain.
-- A page has its title, description and keywords for search engines and «не индексировать».
ALTER TABLE sites
    ADD COLUMN header      jsonb NOT NULL DEFAULT '[]',
    ADD COLUMN footer      jsonb NOT NULL DEFAULT '[]',
    ADD COLUMN page404     uuid REFERENCES site_pages(id) ON DELETE SET NULL,
    ADD COLUMN favicon     text NOT NULL DEFAULT '',
    ADD COLUMN metrika     text NOT NULL DEFAULT '',
    ADD COLUMN analytics   text NOT NULL DEFAULT '',
    ADD COLUMN accent      text NOT NULL DEFAULT '',
    ADD COLUMN font        text NOT NULL DEFAULT '',
    ADD COLUMN domain      text UNIQUE;

ALTER TABLE site_pages
    ADD COLUMN seo_title       text NOT NULL DEFAULT '',
    ADD COLUMN seo_description text NOT NULL DEFAULT '',
    ADD COLUMN seo_keywords    text NOT NULL DEFAULT '',
    ADD COLUMN noindex         boolean NOT NULL DEFAULT false;

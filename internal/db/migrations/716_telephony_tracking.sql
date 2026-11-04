-- «Колл-трекинг»: an external line is the number of an advertising channel (Маркетинг → «Рекламные
-- источники»). A site with go24's script shows it to the visitors the channel brought; a new lead of a call to the line
-- gets the channel's «Источник» and its utm_source, so «Сквозная аналитика» counts it to the channel.
ALTER TABLE telephony_trunks ADD COLUMN ad_source uuid REFERENCES mkt_ad_sources (id) ON DELETE SET NULL;

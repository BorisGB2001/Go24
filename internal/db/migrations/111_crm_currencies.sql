-- «Валюты» of the portal CRM: the currencies of the company, one of them the base one. rate is what one unit costs in
-- the base currency (the base has 1); kanban sums and the analytics count in the base currency by it. auto: the rate
-- comes daily from the Central Bank of Russia (a rate set by hand turns it off). name: empty — the name of the locale.
CREATE TABLE crm_currencies (
    code       text PRIMARY KEY CHECK (code ~ '^[A-Z]{3}$'),
    name       text NOT NULL DEFAULT '',
    rate       numeric(24, 10) NOT NULL DEFAULT 1 CHECK (rate > 0),
    base       boolean NOT NULL DEFAULT false,
    auto       boolean NOT NULL DEFAULT true,
    sort       int NOT NULL DEFAULT 100,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX crm_currencies_base ON crm_currencies (base) WHERE base;

INSERT INTO crm_currencies (code, base, sort) VALUES ('RUB', true, 10), ('USD', false, 20), ('EUR', false, 30), ('CNY', false, 40);
-- the currencies the cards and the catalog already have
INSERT INTO crm_currencies (code)
    SELECT DISTINCT upper(currency) FROM (SELECT currency FROM crm_deals UNION SELECT currency FROM crm_leads
        UNION SELECT currency FROM crm_products UNION SELECT currency FROM crm_invoices) c
    WHERE upper(currency) ~ '^[A-Z]{3}$' ON CONFLICT DO NOTHING;
-- their rates are not known yet: the worker takes them from the CBR at once
UPDATE crm_currencies SET updated_at = '2000-01-01' WHERE NOT base;

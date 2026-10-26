-- «Наблюдатели» of a deal (a smart process's card too) and a lead: they see the card
-- whatever their rights and are told of its comments and stages
CREATE TABLE crm_deal_observers (
	item_id uuid NOT NULL REFERENCES crm_deals(id) ON DELETE CASCADE,
	user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	PRIMARY KEY (item_id, user_id)
);
CREATE INDEX ON crm_deal_observers (user_id);

CREATE TABLE crm_lead_observers (
	item_id uuid NOT NULL REFERENCES crm_leads(id) ON DELETE CASCADE,
	user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	PRIMARY KEY (item_id, user_id)
);
CREATE INDEX ON crm_lead_observers (user_id);

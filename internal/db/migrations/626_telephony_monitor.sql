-- «Текущие звонки» (GO24DEV-821): the right «monitor» of telephony's roles — listening to the conversations going on,
-- whispering and barging in (none, dept — one's departments with the ones below, all). Heads of departments get their
-- departments', everyone else none (a role without the right has none).
UPDATE telephony_roles SET perms = perms || '{"monitor": "dept"}' WHERE code = 'heads';

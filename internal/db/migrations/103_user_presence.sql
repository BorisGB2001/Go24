-- Presence (GO24DEV-276): when a person was last seen in go24 (an open page keeps them «в сети»)
-- and the status they set themselves — '' (online), 'away' («Отошёл») or 'dnd' («Не беспокоить»: no pop-ups
-- and no sounds).
ALTER TABLE users ADD COLUMN seen_at timestamptz;
ALTER TABLE users ADD COLUMN presence text NOT NULL DEFAULT '' CHECK (presence IN ('', 'away', 'dnd'));

-- The application of an incoming webhook (GO24DEV-962): a webhook (a personal API key) registers chat bots of
-- imbot.v2 with botToken lets it; the bots need an owner application, so the webhook gets one —
-- api-only and installed, made on its first imbot.v2.Bot.register. The key revoked or deleted, its bots stay until
-- the application is taken off.
ALTER TABLE apps ADD COLUMN webhook_key text UNIQUE REFERENCES api_keys(key_id) ON DELETE SET NULL;

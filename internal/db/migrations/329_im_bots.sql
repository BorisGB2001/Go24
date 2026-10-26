-- Chat bots of local applications, as imbot.* (GO24DEV-622): an application registers a bot, the bot
-- is a participant of the messenger with its own Matrix account (not an employee: it is in no list of the company);
-- people write to it in a private chat or add it to a group chat, go24 worker reads its chats and queues the events
-- (ONIMBOTMESSAGEADD, ONIMBOTJOINCHAT, ONIMBOTDELETE) to the application's handlers (app_events), the application
-- answers as the bot over REST.
CREATE TABLE im_bots (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    app_id          uuid REFERENCES apps(id) ON DELETE SET NULL, -- NULL: the bot is gone, the worker closes its account
    code            text NOT NULL CHECK (code ~ '^[A-Za-z0-9_.-]{1,50}$'),
    type            text NOT NULL DEFAULT 'B' CHECK (type IN ('B', 'H', 'S')), -- S: gets every message of its group chats
    name            text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    last_name       text NOT NULL DEFAULT '',
    work_position   text NOT NULL DEFAULT '',
    color           text NOT NULL DEFAULT '',
    mxid            text NOT NULL UNIQUE,
    token           text NOT NULL DEFAULT '',
    since           text NOT NULL DEFAULT '', -- the position of the worker's sync of the bot's chats
    user_id         uuid REFERENCES users(id) ON DELETE SET NULL, -- who registered it: whose tokens its events carry
    message_handler text NOT NULL DEFAULT '',
    welcome_handler text NOT NULL DEFAULT '',
    delete_handler  text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX im_bots_code ON im_bots (app_id, upper(code)) WHERE app_id IS NOT NULL;

-- the application taken off: its bots go with it
CREATE FUNCTION go24_im_bots_uninstall() RETURNS trigger AS $$
BEGIN
    UPDATE im_bots SET app_id = NULL WHERE app_id = NEW.id;
    RETURN NULL;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER apps_im_bots AFTER UPDATE OF installed_at ON apps
    FOR EACH ROW WHEN (OLD.installed_at IS NOT NULL AND NEW.installed_at IS NULL) EXECUTE FUNCTION go24_im_bots_uninstall();

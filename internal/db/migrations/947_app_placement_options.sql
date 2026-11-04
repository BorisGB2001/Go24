-- OPTIONS of placement.bind (GO24DEV-977): the widgets of the messenger IM_TEXTAREA, IM_SIDEBAR,
-- IM_CONTEXT_MENU and IM_NAVIGATION, keep iconName, context (USER;CHAT;LINES;CRM;ALL), role (USER|ADMIN), color,
-- width, height and extranet (Y|N) — which chats and who see the application's button.
ALTER TABLE app_placements ADD COLUMN options jsonb NOT NULL DEFAULT '{}';

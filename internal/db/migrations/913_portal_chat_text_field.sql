-- imbot.v2.Chat.TextField.enabled (GO24DEV-964): a bot switches the input field of a chat off — people answer it with
-- its buttons; the chat's page shows a note in place of the field.
ALTER TABLE portal_chats ADD COLUMN text_field boolean NOT NULL DEFAULT true;

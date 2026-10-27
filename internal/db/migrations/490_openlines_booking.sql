-- «Онлайн-запись» in a dialogue of an open line: an operator sends a booking link to the client (kind
-- booking, its key in form_secret, form_done — when the client booked by it); the booking made by it is the
-- dialogue's: its CRM cards, «Клиент записался» in the dialogue (booking_done), its cancellation as a service line.
ALTER TABLE ol_messages ADD COLUMN booking_link_id uuid REFERENCES booking_links(id) ON DELETE SET NULL;
ALTER TABLE ol_messages DROP CONSTRAINT ol_messages_kind_check;
ALTER TABLE ol_messages ADD CONSTRAINT ol_messages_kind_check
    CHECK (kind IN ('', 'welcome', 'offline', 'noanswer', 'close', 'note', 'hidden', 'form', 'form_done', 'bot', 'booking', 'booking_done'));

ALTER TABLE bookings ADD COLUMN ol_session_id uuid REFERENCES ol_sessions(id) ON DELETE SET NULL;

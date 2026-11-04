package crm

import (
	"context"
	"time"
)

// «Контакт-центр»: every channel clients come to the CRM through, on one page — whether it is connected,
// how many of it there are (lines, forms, mailboxes…), how many requests it brought lately, and where it is set up.
// The channels live in their own sections (open lines, telephony, mail, booking, sites); here they are only read.

// Channel is a tile of the contact center.
type Channel struct {
	Key     string `json:"key"`     // widget | telegram | telephony | callback | forms | mail | sms | booking | sites
	Icon    string `json:"-"`       // of the tile
	URL     string `json:"url"`     // where it is set up
	Count   int    `json:"count"`   // connected: active lines, forms, mailboxes…; 0 — not connected
	Errors  int    `json:"errors"`  // of them failing (a Telegram bot, a mailbox that does not sync)
	Recent  int    `json:"recent"`  // requests through it in the last RecentDays (chats, calls, form results, bookings)
	Inbound bool   `json:"inbound"` // counts requests (SMS, mail and sites do not)
}

// RecentDays is the period of Channel.Recent.
const RecentDays = 30

// ContactCenter gives the channels in the order of the page.
func (s *Store) ContactCenter(ctx context.Context, now time.Time) ([]Channel, error) {
	since := now.AddDate(0, 0, -RecentDays)
	list := []Channel{
		{Key: "widget", Icon: "message-circle", URL: "/openlines/widgets", Inbound: true},
		{Key: "telegram", Icon: "send", URL: "/openlines/widgets", Inbound: true},
		{Key: "telephony", Icon: "phone", URL: "/settings/telephony#trunks", Inbound: true},
		{Key: "callback", Icon: "arrow-back-up", URL: "/settings/telephony#callbacks", Inbound: true},
		{Key: "forms", Icon: "file-text", URL: "/crm/forms", Inbound: true},
		{Key: "booking", Icon: "calendar-event", URL: "/booking/links", Inbound: true},
		{Key: "mail", Icon: "mail", URL: "/mail/settings"},
		{Key: "sms", Icon: "messages", URL: "/crm/sms"},
		{Key: "sites", Icon: "world", URL: "/sites"},
	}
	// per channel: how many are connected and failing; and its requests since $1 (inbound ones only)
	count := map[string]string{
		"widget":    `SELECT count(*), 0 FROM ol_widgets WHERE active`,
		"telegram":  `SELECT count(*), count(*) FILTER (WHERE tg_error <> '') FROM ol_widgets WHERE active AND tg_secret IS NOT NULL`,
		"telephony": `SELECT count(*), 0 FROM telephony_trunks`,
		"callback":  `SELECT count(*), 0 FROM telephony_callbacks`,
		"forms":     `SELECT count(*), 0 FROM crm_forms WHERE active`,
		"booking":   `SELECT count(*), 0 FROM booking_links WHERE active`,
		"mail":      `SELECT count(*), count(*) FILTER (WHERE error <> '') FROM mailboxes`,
		"sms":       `SELECT count(*), 0 FROM sms_senders WHERE active`,
		"sites":     `SELECT count(*), 0 FROM sites WHERE published`,
	}
	recent := map[string]string{
		"widget":    `SELECT count(*) FROM ol_sessions WHERE channel = 'widget' AND created_at >= $1`,
		"telegram":  `SELECT count(*) FROM ol_sessions WHERE channel = 'telegram' AND created_at >= $1`,
		"telephony": `SELECT count(*) FROM telephony_calls WHERE from_user IS NULL AND started_at >= $1`,
		"callback":  `SELECT count(*) FROM telephony_callback_requests WHERE created_at >= $1`,
		"forms":     `SELECT count(*) FROM crm_form_results WHERE created_at >= $1`,
		"booking":   `SELECT count(*) FROM bookings WHERE link_id IS NOT NULL AND created_at >= $1`,
	}
	for i := range list {
		c := &list[i]
		if err := s.db.QueryRow(ctx, count[c.Key]).Scan(&c.Count, &c.Errors); err != nil {
			return nil, err
		}
		if c.Inbound {
			if err := s.db.QueryRow(ctx, recent[c.Key], since).Scan(&c.Recent); err != nil {
				return nil, err
			}
		}
	}
	return list, nil
}

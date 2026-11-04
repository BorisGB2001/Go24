package crm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «SMS» of a card: a message to the client's phone through the company's SMS provider (sms package),
// from the card's timeline or by the robot «Отправить SMS клиенту»; the timeline keeps it, sent or refused.

// SMSEvent is the body of a timeline line «sms».
type SMSEvent struct {
	To    string `json:"to"`
	Text  string `json:"text"`
	Error string `json:"error,omitempty"`
}

// ParseSMSEvent reads the body of a line «sms» (ok false — not one).
func ParseSMSEvent(body string) (SMSEvent, bool) {
	var e SMSEvent
	err := json.Unmarshal([]byte(body), &e)
	return e, err == nil && e.To != ""
}

// Phonee is a phone of the card's client and whose it is.
type Phonee struct {
	Phone, Name string
}

// ClientPhones are the phones of the card's client, as ClientEmails: a lead's or contact's own; a company's and its
// contacts'; a deal's contacts' (the main one first) and its company's.
func (s *Store) ClientPhones(ctx context.Context, entity, id string) ([]Phonee, error) {
	var q string
	switch entity {
	case "lead":
		q = `SELECT m.value, coalesce(nullif(l.name, ''), l.title), 0, m.sort FROM crm_leads l JOIN crm_comms m ON m.entity = 'lead' AND m.entity_id = l.id
			WHERE l.id::text = $1`
	case "contact":
		q = `SELECT m.value, x.name, 0, m.sort FROM crm_contacts x JOIN crm_comms m ON m.entity = 'contact' AND m.entity_id = x.id WHERE x.id::text = $1`
	case "company":
		q = `SELECT m.value, c.title, 0, m.sort FROM crm_companies c JOIN crm_comms m ON m.entity = 'company' AND m.entity_id = c.id WHERE c.id::text = $1
			UNION ALL SELECT m.value, x.name, 1, m.sort FROM crm_contacts x JOIN crm_comms m ON m.entity = 'contact' AND m.entity_id = x.id
			WHERE x.company_id::text = $1`
	case "deal":
		q = `SELECT m.value, x.name, CASE WHEN x.id = d.contact_id THEN 0 ELSE 1 END, m.sort FROM crm_deals d
				JOIN crm_contacts x ON x.id = d.contact_id OR x.id IN (SELECT contact_id FROM crm_deal_contacts WHERE deal_id = d.id)
				JOIN crm_comms m ON m.entity = 'contact' AND m.entity_id = x.id
			WHERE d.id::text = $1
			UNION ALL SELECT m.value, c.title, 2, m.sort FROM crm_deals d JOIN crm_companies c ON c.id = d.company_id
				JOIN crm_comms m ON m.entity = 'company' AND m.entity_id = c.id WHERE d.id::text = $1`
	default:
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(ctx, `SELECT phone, name FROM (`+strings.ReplaceAll(q, "JOIN crm_comms m ON", "JOIN crm_comms m ON m.kind = 'phone' AND")+
		`) a(phone, name, o, sort) WHERE trim(phone) <> '' ORDER BY o, lower(name), sort`, id)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Phonee])
	seen, out := map[string]bool{}, list[:0]
	for _, p := range list {
		p.Phone = strings.TrimSpace(p.Phone)
		if k := phoneKey(p.Phone); k != "" && !seen[k] {
			seen[k] = true
			out = append(out, p)
		}
	}
	return out, err
}

// SendSMSTo sends the user's message to the phone from the card and writes it into the card's timeline; a provider's
// refusal is written there too and is the error.
func (s *Store) SendSMSTo(ctx context.Context, userID, entity, id, phone, text string) error {
	if s.SendSMS == nil {
		return i18n.E("err.sms_no_sender")
	}
	if err := clean(&text, 1000); err != nil || text == "" {
		return i18n.E("err.sms_text")
	}
	var ok bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_`+table(entity)+` WHERE id::text = $1)`, id).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return s.smsEvent(ctx, userID, entity, id, strings.TrimSpace(phone), text)
}

// smsEvent sends and writes the line «sms» (errors of the input — before anything is sent — are not written).
func (s *Store) smsEvent(ctx context.Context, userID, entity, id, phone, text string) error {
	sendErr := s.SendSMS(ctx, userID, entity, id, phone, text)
	if ie := (*i18n.Error)(nil); errors.As(sendErr, &ie) {
		return sendErr
	}
	e := SMSEvent{To: phone, Text: text}
	if sendErr != nil {
		e.Error = sendErr.Error()
	}
	b, _ := json.Marshal(e)
	if err := s.event(ctx, s.db, entity, id, userID, "sms", string(b)); err != nil {
		return err
	}
	if sendErr != nil {
		return i18n.E("err.sms_failed", e.Error)
	}
	return nil
}

// robotSMS writes the robot's text to the client's first phone (the lead's; the deal's contact's, or else its
// company's).
func (s *Store) robotSMS(ctx context.Context, actorID string, c card, text string) error {
	if s.SendSMS == nil {
		return i18n.E("err.sms_no_sender")
	}
	phones, err := s.ClientPhones(ctx, c.entity, c.id)
	if err != nil {
		return err
	}
	if len(phones) == 0 {
		return i18n.E("err.robot_phone")
	}
	return s.smsEvent(ctx, actorID, c.entity, c.id, phones[0].Phone, text)
}

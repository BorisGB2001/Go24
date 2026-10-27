package openlines

import (
	"context"
	"strings"

	"go24/internal/crm"
)

// The dialogue and CRM: a new dialogue of a line with «Создавать лид автоматически» becomes a lead
// whose responsible is the dialogue's operator; a visitor whose phone or e-mail is a contact's (or an open lead's) is
// bound to that card instead of a second one; the card's timeline has «Диалог открытой линии» with a link to the chat.

// client is the CRM card with the phone or e-mail: a contact first, then a lead not yet converted.
func (s *Store) client(ctx context.Context, phone, email string) (entity, id string, err error) {
	if s.CRM == nil || (strings.TrimSpace(phone) == "" && strings.TrimSpace(email) == "") {
		return "", "", nil
	}
	list, err := s.CRM.Duplicates(ctx, "lead", "", "", phone, email)
	if err != nil {
		return "", "", err
	}
	for _, want := range []string{"contact", "lead"} {
		for _, d := range list {
			if d.Entity == want {
				return d.Entity, d.ID, nil
			}
		}
	}
	return "", "", nil
}

// bind ties the dialogue to a lead or a contact, with a line in the card's timeline.
func (s *Store) bind(ctx context.Context, sessionID, entity, id, author string, auto bool) error {
	col := map[string]string{"lead": "lead_id", "contact": "contact_id"}[entity]
	if col == "" {
		return nil
	}
	tag, err := s.db.Exec(ctx, `UPDATE ol_sessions SET `+col+` = $2, lead_auto = CASE WHEN $4 = 'lead' THEN $3 ELSE lead_auto END WHERE id::text = $1 AND `+col+` IS DISTINCT FROM $2::uuid`,
		sessionID, id, auto, entity)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return s.CRM.AddEvent(ctx, entity, id, author, "openline", sessionID)
}

// autoLead is the line's own lead of a dialogue the visitor has just started (or the client they already are).
func (s *Store) autoLead(ctx context.Context, w Widget, sessionID string) error {
	if !w.CRMAuto || s.CRM == nil {
		return nil
	}
	var bound bool
	var name, phone, email, creator string
	var operator *string
	if err := s.db.QueryRow(ctx, `SELECT s.lead_id IS NOT NULL OR s.contact_id IS NOT NULL, s.name, s.phone, s.email, s.operator_id::text, w.created_by::text
		FROM ol_sessions s JOIN ol_widgets w ON w.id = s.widget_id WHERE s.id::text = $1`, sessionID).Scan(&bound, &name, &phone, &email, &operator, &creator); err != nil || bound {
		return err
	}
	entity, id, err := s.client(ctx, phone, email)
	if err != nil {
		return err
	}
	if entity != "" {
		if err := s.bind(ctx, sessionID, entity, id, creator, false); err != nil {
			return err
		}
		return s.forward(ctx, sessionID)
	}
	l := crm.Lead{Name: name, Phone: phone, Email: email}
	l.Title = s.tr("ol.lead_title", w.Name, Session{ID: sessionID, Name: name}.Title())
	l.Source, l.Origin.Code = s.tr("ol.lead_source"), "OPENLINE"
	if operator != nil {
		l.Responsible.ID = *operator
	}
	lead, err := s.CRM.SaveLead(ctx, creator, l)
	if err != nil {
		return err
	}
	return s.bind(ctx, sessionID, "lead", lead, creator, true)
}

// follow gives the line's own lead to the dialogue's operator, when another one takes or gets the dialogue.
func (s *Store) follow(ctx context.Context, sessionID string) error {
	if s.CRM == nil {
		return nil
	}
	var lead, operator *string
	if err := s.db.QueryRow(ctx, `SELECT lead_id::text, operator_id::text FROM ol_sessions WHERE id::text = $1 AND lead_auto`, sessionID).Scan(&lead, &operator); err != nil || lead == nil || operator == nil {
		return nil
	}
	l, err := s.CRM.Lead(ctx, *lead)
	if err != nil || l.Responsible.ID == *operator {
		return nil // the lead gone or already theirs
	}
	return s.CRM.SetResponsible(ctx, *operator, "lead", *lead, *operator)
}

// fill gives the line's own lead the name, phone and e-mail the visitor has left, where it has none.
func (s *Store) fill(ctx context.Context, userID, leadID, name, phone, email string) error {
	l, err := s.CRM.Lead(ctx, leadID)
	if err != nil {
		return nil
	}
	changed := false
	for _, f := range []struct {
		to *string
		v  string
	}{{&l.Name, name}, {&l.Phone, phone}, {&l.Email, email}} {
		if *f.to == "" && f.v != "" {
			*f.to, changed = f.v, true
		}
	}
	if !changed {
		return nil
	}
	_, err = s.CRM.SaveLead(ctx, userID, l)
	return err
}

// ChatOf is the latest dialogue bound to a lead or a contact ("" — none): «Открыть чат» on its card.
func (s *Store) ChatOf(ctx context.Context, entity, id string) (string, error) {
	col := map[string]string{"lead": "lead_id", "contact": "contact_id"}[entity]
	if col == "" {
		return "", nil
	}
	var chat string
	err := s.db.QueryRow(ctx, `SELECT coalesce((SELECT id::text FROM ol_sessions WHERE `+col+`::text = $1 ORDER BY last_at DESC LIMIT 1), '')`, id).Scan(&chat)
	return chat, err
}

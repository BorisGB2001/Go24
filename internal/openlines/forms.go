package openlines

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/crm"
	"go24/internal/i18n"
)

// A CRM form in the dialogue «Формы» of the operator's panel: the operator sends an active CRM form,
// the client fills it in the widget (or by the link in Telegram); the filling's lead or contact is bound to the
// dialogue, a lead the dialogue already has gets the answers instead of a second lead.

// SendForm sends the CRM form to the dialogue's client as the operator's answer; link is the form's address for the
// client by its token and the key tying the filling to this dialogue.
func (s *Store) SendForm(ctx context.Context, userID, id, formID string, link func(token, secret string) string) error {
	if s.CRM == nil {
		return ErrNotFound
	}
	f, err := s.CRM.Form(ctx, formID)
	if err != nil {
		return err
	}
	if !f.Active {
		return i18n.E("err.ol_form_off")
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	secret := hex.EncodeToString(b)
	body := s.tr("ol.form_body", formTitle(f)) + "\n" + link(f.Token, secret)
	return s.answer(ctx, userID, id, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO ol_messages (session_id, author_id, body, kind, form_id, form_secret) VALUES ($1::uuid, $2, $3, 'form', $4, $5)`,
			id, userID, body, f.ID, secret)
		return err
	})
}

func formTitle(f crm.Form) string {
	if f.Title != "" {
		return f.Title
	}
	return f.Name
}

// FormLink is the dialogue a sent form's link belongs to and the dialogue's lead ("" — none).
type FormLink struct {
	Session, LeadID string
}

// FormFor is the dialogue of the form's link key (ErrNotFound — not a link sent in a dialogue).
func (s *Store) FormFor(ctx context.Context, formID, secret string) (FormLink, error) {
	var l FormLink
	if secret == "" {
		return l, ErrNotFound
	}
	err := s.db.QueryRow(ctx, `SELECT s.id::text, coalesce(s.lead_id::text, '') FROM ol_messages m JOIN ol_sessions s ON s.id = m.session_id
		WHERE m.form_secret = $1 AND m.form_id::text = $2 AND m.kind = 'form' AND NOT s.spam`, secret, formID).Scan(&l.Session, &l.LeadID)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, ErrNotFound
	}
	return l, err
}

// FormFilled: the client has filled the form sent by the link key — the dialogue gets «Форма заполнена» (the client
// sees it too) and a service line of the cards made, the client's name, phone and e-mail it has none of, the cards (if
// it is not bound yet); the operators are told.
func (s *Store) FormFilled(ctx context.Context, secret string, res crm.FormResult) error {
	got := map[string]string{}
	for _, a := range res.Answers {
		got[a.Key] = a.Value
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id, formID, title, creator string
	if err := tx.QueryRow(ctx, `UPDATE ol_messages m SET form_done = now() FROM crm_forms f, ol_sessions s, ol_widgets w
		WHERE m.form_secret = $1 AND m.kind = 'form' AND f.id = m.form_id AND s.id = m.session_id AND w.id = s.widget_id
		RETURNING m.session_id::text, f.id::text, CASE WHEN f.title <> '' THEN f.title ELSE f.name END, w.created_by::text`, secret).Scan(&id, &formID, &title, &creator); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	done := s.tr("ol.form_done", title)
	if _, err := tx.Exec(ctx, `INSERT INTO ol_messages (session_id, body, kind, form_id) VALUES ($1::uuid, $2, 'form_done', $3)`, id, done, formID); err != nil {
		return err
	}
	var cards []string
	for _, c := range []struct{ id, table, col, key string }{
		{res.LeadID, "crm_leads", "title", "ol.form_lead"}, {res.ContactID, "crm_contacts", "name", "ol.form_contact"}, {res.DealID, "crm_deals", "title", "ol.form_deal"},
	} {
		var name string
		if c.id != "" && tx.QueryRow(ctx, `SELECT `+c.col+` FROM `+c.table+` WHERE id::text = $1`, c.id).Scan(&name) == nil {
			cards = append(cards, s.tr(c.key, name))
		}
	}
	if len(cards) > 0 {
		if err := s.note(ctx, tx, id, "ol.note_form", title, strings.Join(cards, ", ")); err != nil {
			return err
		}
	}
	name := strings.TrimSpace(got["name"] + " " + got["last_name"])
	if _, err := tx.Exec(ctx, `UPDATE ol_sessions SET name = CASE WHEN name = '' THEN left($2, 200) ELSE name END,
		phone = CASE WHEN phone = '' THEN left($3, 100) ELSE phone END, email = CASE WHEN email = '' THEN left($4, 200) ELSE email END,
		unread = unread + 1, last_at = now() WHERE id::text = $1`, id, name, got["phone"], got["email"]); err != nil {
		return err
	}
	if err := notifyOperators(ctx, tx, id, done); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	var lead, contact bool
	if err := s.db.QueryRow(ctx, `SELECT lead_id IS NOT NULL, contact_id IS NOT NULL FROM ol_sessions WHERE id::text = $1`, id).Scan(&lead, &contact); err != nil {
		return err
	}
	switch {
	case res.ContactID != "" && !contact:
		return s.bind(ctx, id, "contact", res.ContactID, creator, false)
	case res.LeadID != "" && !lead && !contact:
		return s.bind(ctx, id, "lead", res.LeadID, creator, false)
	}
	return nil
}

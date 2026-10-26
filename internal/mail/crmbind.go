package mail

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ---------- «Почта» → CRM by itself ----------

// As the mailbox settings «Привязывать письма к CRM» and «Создавать лид для новых адресов»: a letter that
// comes or goes is put into the cards of the conversation it continues; with crm_bind — into the contacts, companies
// and open leads of the correspondent's address and the open deals of those contacts and companies; with crm_leads —
// a letter from an address the CRM does not know (not a mailing) makes a lead (the Lead hook).

// NewLead is the lead a letter from an unknown address makes.
type NewLead struct {
	Title, Name, Email, Comment string
}

// BoxCRM are a mailbox's CRM settings.
type BoxCRM struct {
	Bind, Leads bool
}

// MailboxCRM: the mailbox's CRM settings (the user's own only).
func (s *Store) MailboxCRM(ctx context.Context, userID, mailboxID string) (BoxCRM, error) {
	var b BoxCRM
	err := s.db.QueryRow(ctx, `SELECT crm_bind, crm_leads FROM mailboxes WHERE id::text = $1 AND user_id::text = $2`, mailboxID, userID).
		Scan(&b.Bind, &b.Leads)
	if err == pgx.ErrNoRows {
		err = ErrNotFound
	}
	return b, err
}

// SetMailboxCRM sets them.
func (s *Store) SetMailboxCRM(ctx context.Context, userID, mailboxID string, b BoxCRM) error {
	tag, err := s.db.Exec(ctx, `UPDATE mailboxes SET crm_bind = $3, crm_leads = $4 WHERE id::text = $1 AND user_id::text = $2`,
		mailboxID, userID, b.Bind, b.Leads)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// bindCRM puts a letter just come (or gone: in «Отправленные») into its CRM cards; a failure is only reported.
func (s *Store) bindCRM(ctx context.Context, owner, letterID string, bulk bool) {
	if err := s.crmCards(ctx, owner, letterID, bulk); err != nil && s.Log != nil {
		s.Log("mail: crm binding", letterID, err)
	}
}

func (s *Store) crmCards(ctx context.Context, owner, letterID string, bulk bool) error {
	var from, name, to, cc, thread, mailboxID, role, subject, body string
	var box BoxCRM
	err := s.db.QueryRow(ctx, `SELECT x.from_addr, x.from_name, x.to_addr, x.cc_addr, x.thread, x.mailbox_id::text, f.role, x.subject,
			left(x.body, 9000), m.crm_bind, m.crm_leads
		FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = x.mailbox_id
		WHERE x.id::text = $1 AND m.user_id::text = $2`, letterID, owner).
		Scan(&from, &name, &to, &cc, &thread, &mailboxID, &role, &subject, &body, &box.Bind, &box.Leads)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	type card struct{ entity, id string }
	var cards []card
	collect := func(rows pgx.Rows, err error) error {
		if err != nil {
			return err
		}
		list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (card, error) {
			var c card
			return c, r.Scan(&c.entity, &c.id)
		})
		cards = append(cards, list...)
		return err
	}
	if thread != "" { // the answers of a conversation in the CRM go into its cards, whatever the settings
		if err := collect(s.db.Query(ctx, `SELECT DISTINCT entity, entity_id::text FROM crm_mail WHERE mailbox_id::text = $1 AND thread = $2`,
			mailboxID, thread)); err != nil {
			return err
		}
	}
	outgoing := role == "sent"
	own, err := s.ownAddresses(ctx, owner)
	if err != nil {
		return err
	}
	var addrs []string // the correspondents: the sender of a letter come, the recipients of one gone
	if outgoing {
		for _, list := range []string{to, cc} {
			l, _ := addresses(list)
			for _, a := range l {
				if a := strings.ToLower(a.Address); !own[a] {
					addrs = append(addrs, a)
				}
			}
		}
	} else if a := strings.ToLower(strings.TrimSpace(from)); a != "" && !own[a] {
		addrs = append(addrs, a)
	}
	if box.Bind && !bulk && len(addrs) > 0 {
		if err := collect(s.db.Query(ctx, `WITH c AS (SELECT id FROM crm_contacts WHERE lower(trim(email)) = ANY($1)),
				co AS (SELECT id FROM crm_companies WHERE lower(trim(email)) = ANY($1))
			SELECT 'contact', id::text FROM c
			UNION SELECT 'company', id::text FROM co
			UNION SELECT 'lead', l.id::text FROM crm_leads l JOIN crm_stages st ON st.id = l.stage_id
				WHERE st.semantic = 'process' AND l.deal_id IS NULL AND lower(trim(l.email)) = ANY($1)
			UNION SELECT 'deal', d.id::text FROM crm_deals d JOIN crm_stages st ON st.id = d.stage_id
				WHERE st.semantic = 'process' AND (d.contact_id IN (SELECT id FROM c) OR d.company_id IN (SELECT id FROM co)
					OR EXISTS (SELECT 1 FROM crm_deal_contacts dc WHERE dc.deal_id = d.id AND dc.contact_id IN (SELECT id FROM c)))`, addrs)); err != nil {
			return err
		}
	}
	if len(cards) == 0 && box.Leads && !outgoing && !bulk && len(addrs) == 1 && s.Lead != nil {
		var known bool // a contact, company or lead of the address there is (a lead won or lost too): no new lead
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_contacts WHERE lower(trim(email)) = $1)
			OR EXISTS (SELECT 1 FROM crm_companies WHERE lower(trim(email)) = $1) OR EXISTS (SELECT 1 FROM crm_leads WHERE lower(trim(email)) = $1)`,
			addrs[0]).Scan(&known); err != nil {
			return err
		}
		if !known {
			if name == "" {
				name, _, _ = strings.Cut(addrs[0], "@")
			}
			title := strings.TrimSpace(subject)
			if title == "" {
				title = name
			}
			id, err := s.Lead(ctx, owner, NewLead{Title: clip(title, 300), Name: clip(name, 300), Email: addrs[0], Comment: body})
			if err != nil {
				return err
			}
			if err := s.SetLead(ctx, letterID, id); err != nil {
				return err
			}
			cards = append(cards, card{"lead", id})
		}
	}
	for _, c := range cards {
		if _, err := s.LinkCRM(ctx, owner, letterID, c.entity, c.id); err != nil {
			return err
		}
	}
	return nil
}

// ownAddresses are the user's mailboxes' addresses (lower case).
func (s *Store) ownAddresses(ctx context.Context, userID string) (map[string]bool, error) {
	rows, err := s.db.Query(ctx, `SELECT lower(email) FROM mailboxes WHERE user_id::text = $1`, userID)
	if err != nil {
		return nil, err
	}
	list, err := pgxCollectStrings(rows)
	own := map[string]bool{}
	for _, a := range list {
		own[a] = true
	}
	return own, err
}

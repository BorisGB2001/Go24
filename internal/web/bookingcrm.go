package web

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"strings"

	"go24/internal/auth"
	"go24/internal/booking"
	"go24/internal/crm"
)

// «Онлайн-запись» and CRM: a booking is of the client's contact, company and deal; a card bound to it gets
// «Запись клиента» in its timeline (shown as the booking is now), the card has its bookings and «Записать»; «Создать
// сделку» from a booking makes a deal of its client; a client who books by a link becomes a contact. The bound deal's
// triggers hear of the booking: made, confirmed, the client come or not, cancelled.

// bookingCRM takes the cards of the booking's form (contact_id, company_id, deal_id) the user may read; a deal brings
// its contact and company, a contact its company, and a contact fills the client left empty.
func (s *Server) bookingCRM(r *http.Request, sess auth.Session, b *booking.Booking) error {
	if s.CRM == nil {
		return nil
	}
	b.ContactID, b.CompanyID, b.DealID = r.FormValue("contact_id"), r.FormValue("company_id"), r.FormValue("deal_id")
	return s.bookingCards(r, sess, b)
}

// bookingCards checks the cards of b (ContactID, CompanyID, DealID) the user may read and fills them in, as bookingCRM.
func (s *Server) bookingCards(r *http.Request, sess auth.Session, b *booking.Booking) error {
	if s.CRM == nil {
		b.ContactID, b.CompanyID, b.DealID = "", "", ""
		return nil
	}
	for kind, id := range map[string]string{"contacts": b.ContactID, "companies": b.CompanyID, "deals": b.DealID} {
		if id != "" {
			if err := s.crmCheck(r, sess, kind, id, "read"); err != nil {
				return err
			}
		}
	}
	if b.DealID != "" {
		d, err := s.CRM.Deal(r.Context(), b.DealID)
		if err != nil {
			return err
		}
		b.ContactID, b.CompanyID = cmp.Or(b.ContactID, d.Contact.ID), cmp.Or(b.CompanyID, d.Company.ID)
	}
	if b.ContactID != "" {
		x, err := s.CRM.Contact(r.Context(), b.ContactID)
		if err != nil {
			return err
		}
		b.CompanyID = cmp.Or(b.CompanyID, x.Company.ID)
		if b.ClientName == "" && b.ClientPhone == "" {
			b.ClientName, b.ClientPhone, b.ClientEmail = x.Name, x.Phone, cmp.Or(b.ClientEmail, x.Email)
		}
	}
	return nil
}

// bookingEvents writes «Запись клиента» in the timeline of each card the booking is bound to now and was not before.
func (s *Server) bookingEvents(ctx context.Context, by string, old, b booking.Booking) {
	if s.CRM == nil {
		return
	}
	for _, c := range [][3]string{{"contact", old.ContactID, b.ContactID}, {"company", old.CompanyID, b.CompanyID}, {"deal", old.DealID, b.DealID}} {
		if c[2] != "" && c[2] != c[1] {
			if err := s.CRM.AddEvent(ctx, c[0], c[2], by, "booking", b.ID); err != nil {
				s.Log.Warn("booking crm event", "err", err)
			}
		}
	}
	s.bookingTriggers(ctx, old, b)
}

// bookingTriggerOf is the deal's trigger a booking's status fires.
var bookingTriggerOf = map[string]string{"confirmed": "booking_confirmed", "came": "booking_came", "no_show": "booking_no_show",
	"cancelled": "booking_cancelled"}

// bookingTriggers fires the triggers of the booking's deal: «booking» when the deal is bound to it now and was not
// before, and the one of its status when that changed.
func (s *Server) bookingTriggers(ctx context.Context, old, b booking.Booking) {
	if s.CRM == nil || b.DealID == "" {
		return
	}
	var kinds []string
	if b.DealID != old.DealID && b.Status != "cancelled" {
		kinds = append(kinds, "booking")
	}
	if k := bookingTriggerOf[b.Status]; k != "" && b.Status != old.Status {
		kinds = append(kinds, k)
	}
	for _, k := range kinds {
		if err := s.CRM.Fire(ctx, "deal", b.DealID, k); err != nil && !errors.Is(err, crm.ErrNotFound) {
			s.Log.Warn("booking crm trigger", "err", err)
		}
	}
}

// bookingSetStatus changes the booking's status and fires its deal's trigger.
func (s *Server) bookingSetStatus(ctx context.Context, by, id, status string) error {
	old, err := s.Booking.Booking(ctx, id)
	if err != nil {
		return err
	}
	if err := s.Booking.SetStatus(ctx, by, id, status); err != nil {
		return err
	}
	b := old
	b.Status = status
	s.bookingTriggers(ctx, old, b)
	return nil
}

// bookingDeal: «Создать сделку» — a deal of the booking's client (its contact and company; a client not in CRM yet
// becomes a contact), bound to the booking; then the deal's card.
func (s *Server) bookingDeal(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	b, err := s.Booking.Booking(ctx, r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/booking")
		return
	}
	back := "/booking?date=" + b.Start.Local().Format("2006-01-02") + "&b=" + b.ID
	if s.CRM == nil {
		http.NotFound(w, r)
		return
	}
	if b.DealID != "" {
		http.Redirect(w, r, "/crm/deals/"+b.DealID, http.StatusSeeOther)
		return
	}
	if err := s.CRM.Check(ctx, sess.UserID, "deal", "write", sess.UserID); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	old := b
	if b.ContactID == "" && (b.ClientName != "" || b.ClientPhone != "" || b.ClientEmail != "") {
		if err = s.CRM.Check(ctx, sess.UserID, "contact", "write", sess.UserID); err == nil {
			b.ContactID, _, err = s.CRM.ClientContact(ctx, sess.UserID, b.ClientName, b.ClientPhone, b.ClientEmail, crm.Dict{Code: "BOOKING"})
		}
		if err != nil {
			s.taskErr(w, r, err, back)
			return
		}
	}
	if b.ContactID != "" && b.CompanyID == "" {
		if x, err := s.CRM.Contact(ctx, b.ContactID); err == nil {
			b.CompanyID = x.Company.ID
		}
	}
	title := s.tr(r, "booking.deal_title", cmp.Or(b.ClientName, b.ClientPhone), b.ResourceName, b.Start.Local().Format("02.01.2006 15:04"))
	pipes, err := s.CRM.Pipelines(ctx) // the first pipeline, as a new deal of a CRM form
	if err == nil && len(pipes) == 0 {
		err = crm.ErrNotFound
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	d := crm.Deal{Title: title, PipelineID: pipes[0].ID, Comment: b.Note, Origin: crm.Dict{Code: "BOOKING"}, Responsible: crm.Ref{ID: sess.UserID},
		Contacts: []crm.Ref{{ID: b.ContactID}}, Company: crm.Ref{ID: b.CompanyID}}
	if _, cur := b.Total(); cur != "" { // the deal is in the currency of its services
		d.Currency = cur
	}
	if b.DealID, err = s.CRM.SaveDeal(ctx, sess.UserID, d); err == nil && len(b.Services) > 0 {
		_, err = s.CRM.SetProductRows(ctx, sess.UserID, "deal", b.DealID, s.bookingRows(ctx, b))
	}
	if err == nil {
		_, err = s.Booking.SaveBooking(ctx, sess.UserID, b)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.bookingEvents(ctx, sess.UserID, old, b)
	http.Redirect(w, r, "/crm/deals/"+b.DealID, http.StatusSeeOther)
}

// bookingRows: the services of a booking as product rows of its deal, at the prices they were booked at (the VAT
// and the unit — of the catalog's service, while it is there).
func (s *Server) bookingRows(ctx context.Context, b booking.Booking) []crm.ProductRow {
	rows := make([]crm.ProductRow, 0, len(b.Services))
	for _, x := range b.Services {
		row := crm.ProductRow{ProductID: x.ProductID, Name: x.Name, Price: x.Price, Quantity: 1, VATIncluded: true}
		if p, err := s.CRM.Product(ctx, x.ProductID); x.ProductID != "" && err == nil {
			row.Unit, row.VATRate, row.VATIncluded = p.Unit, p.VATRate, p.VATIncluded
		}
		rows = append(rows, row)
	}
	return rows
}

// bookingClientCard makes the client of a booking by a link a contact of the responsible one (the one with the
// phone or e-mail if there is), bound to the booking.
func (s *Server) bookingClientCard(ctx context.Context, resp string, b booking.Booking) {
	if s.CRM == nil || resp == "" || b.ContactID != "" {
		return
	}
	old := b
	var err error
	if b.ContactID, _, err = s.CRM.ClientContact(ctx, resp, b.ClientName, b.ClientPhone, b.ClientEmail, crm.Dict{Code: "BOOKING"}); err == nil {
		if x, cerr := s.CRM.Contact(ctx, b.ContactID); cerr == nil {
			b.CompanyID = x.Company.ID
		}
		_, err = s.Booking.SaveBooking(ctx, resp, b)
	}
	if err != nil {
		s.Log.Warn("booking client contact", "err", err)
		return
	}
	s.bookingEvents(ctx, resp, old, b)
}

// bookingCRMPreset: the card a booking is made for («Записать» in a CRM card, ?crm=/crm/contacts/id) — its client
// and cards for the new booking's form; nil when there is none or the user may not read it.
func (s *Server) bookingCRMPreset(r *http.Request, sess auth.Session) map[string]string {
	path := strings.TrimPrefix(r.URL.Query().Get("crm"), "/crm/")
	kind, id, ok := strings.Cut(path, "/")
	if !ok || s.CRM == nil || s.crmCheck(r, sess, kind, id, "read") != nil {
		return nil
	}
	p := map[string]string{"path": "/crm/" + kind + "/" + id}
	switch crmEntities[kind] {
	case "contact":
		x, err := s.CRM.Contact(r.Context(), id)
		if err != nil {
			return nil
		}
		p["title"], p["name"], p["phone"], p["email"] = x.Name, x.Name, x.Phone, x.Email
		p["contact_id"], p["contact"], p["company_id"], p["company"] = x.ID, x.Name, x.Company.ID, x.Company.Name
	case "company":
		c, err := s.CRM.Company(r.Context(), id)
		if err != nil {
			return nil
		}
		p["title"], p["name"], p["phone"], p["email"], p["company_id"], p["company"] = c.Title, c.Title, c.Phone, c.Email, c.ID, c.Title
	case "deal":
		d, err := s.CRM.Deal(r.Context(), id)
		if err != nil {
			return nil
		}
		p["title"], p["deal_id"], p["deal"], p["company_id"], p["company"] = d.Title, d.ID, d.Title, d.Company.ID, d.Company.Name
		if d.Contact.ID != "" {
			if x, err := s.CRM.Contact(r.Context(), d.Contact.ID); err == nil {
				p["contact_id"], p["contact"], p["name"], p["phone"], p["email"] = x.ID, x.Name, x.Name, x.Phone, x.Email
			}
		}
	default:
		return nil
	}
	return p
}

// bookingCardBack: after a booking made from a CRM card, back to the card.
func bookingCardBack(r *http.Request, back string) string {
	if p := r.FormValue("crm_back"); strings.HasPrefix(p, "/crm/") && !strings.ContainsAny(p, "?#\\") {
		return p + "#bookings"
	}
	return back
}

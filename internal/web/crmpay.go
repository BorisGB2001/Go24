package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/paysys"
)

// ---------- «Платёжные системы» of the CRM (/crm/paysystems) and the client paying an invoice by its link ----------

// As: administrators connect the company's payment services (ЮKassa, Т-Банк); the invoice by the
// client's link (/q/{token}) has «Оплатить» — the client pays what is left on the service's page and comes back;
// the service's notice (/pay/{system}/notify), the client's return or the worker's check confirm the payment at the
// service, and the invoice takes it: «Оплаты», the card's timeline, «Оплачен» with its robots' trigger.

func (s *Server) crmPayPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	data := map[string]any{"Kind": "paysystems", "Kinds": paysys.Kinds, "Admin": sess.Admin, "Base": s.BaseURL}
	if err := s.crmTabs(r, sess, data); err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	if s.Pay != nil {
		list, err := s.Pay.Systems(r.Context())
		if err != nil {
			s.taskErr(w, r, err, "/crm")
			return
		}
		data["Systems"] = list
	}
	s.render(w, r, "crmpay", page{TitleKey: "crm.pay.title", Nav: "crm_deals", Flash: r.URL.Query().Get("msg"), Data: data})
}

// crmPaySave: the administrator adds or changes a service, or deletes it.
func (s *Server) crmPaySave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/crm/paysystems"
	if !sess.Admin || s.Pay == nil {
		s.taskErr(w, r, i18n.E("err.forbidden"), back)
		return
	}
	var err error
	switch r.FormValue("op") {
	case "save":
		_, err = s.Pay.SaveSystem(r.Context(), paysys.System{ID: r.FormValue("id"), Kind: r.FormValue("kind"), Name: r.FormValue("name"),
			Login: r.FormValue("login"), Active: r.FormValue("active") != ""}, r.FormValue("secret"))
	case "delete":
		err = s.Pay.DeleteSystem(r.Context(), r.FormValue("id"))
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "crm.saved")), http.StatusSeeOther)
}

// payFor is what the invoice by the client's link offers to pay through: the active services, if anything is left
// to pay (nil — nothing).
func (s *Server) payFor(ctx context.Context, v crm.Invoice) ([]paysys.System, error) {
	if s.Pay == nil || v.Due() <= 0 {
		return nil, nil
	}
	return s.Pay.Active(ctx)
}

// publicInvoicePay: «Оплатить» of the invoice by the client's link — a payment of what is left through the chosen
// service; the client goes to the service's page and comes back to the invoice.
func (s *Server) publicInvoicePay(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	v, err := s.CRM.PublicInvoice(r.Context(), token)
	if err != nil || s.Pay == nil || v.Due() <= 0 {
		http.Redirect(w, r, "/q/"+url.PathEscape(token), http.StatusSeeOther)
		return
	}
	base := s.BaseURL
	p, err := s.Pay.Start(r.Context(), r.FormValue("system"), paysys.Start{
		InvoiceID: v.ID, Amount: v.Due(), Currency: v.Currency,
		Description: s.tr(r, "crm.pay.description", s.tr(r, "crm.doc.invoice"), v.Code()),
		ReturnURL:   base + "/q/" + url.PathEscape(token) + "?pay={id}",
		NotifyURL:   base + "/pay/" + url.PathEscape(r.FormValue("system")) + "/notify",
	})
	if err != nil {
		if s.Log != nil {
			s.Log.Warn("online payment not started", "invoice", v.ID, "err", err)
		}
		// the service's refusal is the company's to see (the invoice's «Оплаты»), the client is told it did not work
		http.Redirect(w, r, "/q/"+url.PathEscape(token)+"?payerr=1", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, p.URL, http.StatusSeeOther)
}

// payNotify takes a service's notice about a payment: the payment is checked at the service and, paid, taken into
// its invoice. The service gets OK for a notice it need not repeat.
func (s *Server) payNotify(w http.ResponseWriter, r *http.Request) {
	if s.Pay == nil {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	system := r.PathValue("system")
	ext, err := s.Pay.Notice(r.Context(), system, body)
	if err == nil {
		var p paysys.Payment
		if p, err = s.Pay.ByExternal(r.Context(), system, ext); err == nil {
			_, err = s.payCheck(r.Context(), p.ID)
		}
	}
	switch {
	case errors.Is(err, paysys.ErrNotFound):
		// not a payment of go24 (or a forged notice): nothing to repeat
	case err != nil:
		if s.Log != nil {
			s.Log.Warn("payment notice", "system", system, "err", err)
		}
		http.Error(w, "try later", http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("OK"))
}

// payCheck asks the service how the payment is and, seen paid now, takes it into the invoice — once; the invoice
// paid or cancelled meanwhile keeps the reason with the payment.
func (s *Server) payCheck(ctx context.Context, id string) (paysys.Payment, error) {
	p, paid, err := s.Pay.Check(ctx, id)
	if err != nil || !paid {
		return p, err
	}
	v, err := s.CRM.Invoice(ctx, p.InvoiceID)
	if err == nil {
		err = s.CRM.AddPayment(ctx, v.Responsible.ID, v.ID, crm.Payment{Amount: p.Amount, Comment: p.System + " · " + p.ExtID})
	}
	if err != nil {
		lang := s.I18n.Match("")
		if s.Portal != nil && s.I18n.Supported(s.Portal.Current().Lang) {
			lang = s.Portal.Current().Lang
		}
		if ferr := s.Pay.Failed(ctx, p.ID, s.I18n.Err(lang, err)); ferr != nil {
			return p, ferr
		}
		if s.Log != nil {
			s.Log.Warn("online payment not taken into the invoice", "payment", p.ID, "err", err)
		}
	}
	return p, nil
}

// PayCheckDue checks again the payments of the last day the services have not told about (go24 worker).
func (s *Server) PayCheckDue(ctx context.Context) {
	if s.Pay == nil {
		return
	}
	list, err := s.Pay.Pending(ctx)
	for _, p := range list {
		if _, cerr := s.payCheck(ctx, p.ID); cerr != nil && ctx.Err() == nil {
			err = cerr
		}
	}
	if err != nil && ctx.Err() == nil && s.Log != nil {
		s.Log.Warn("online payments check", "err", err)
	}
}

// payReturn: the client came back from the service's page — the payment is checked at once; what the invoice says
// about it.
func (s *Server) payReturn(r *http.Request, v crm.Invoice) (string, error) {
	id := r.URL.Query().Get("pay")
	if id == "" || s.Pay == nil {
		return "", nil
	}
	p, err := s.Pay.Payment(r.Context(), id)
	if errors.Is(err, paysys.ErrNotFound) || err == nil && p.InvoiceID != v.ID {
		return "", nil
	}
	if err == nil {
		p, err = s.payCheck(r.Context(), id)
	}
	if err != nil {
		return "pending", nil // the service did not answer now: its notice will come
	}
	return p.Status, nil
}

// GET /api/v1/crm/paysystems — the payment services (without their keys); ?invoice=ID — the payments of an invoice
// started online, the last first.
func (s *Server) apiPaySystems(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.Pay == nil {
		writeJSON(w, http.StatusOK, []paysys.System{})
		return nil
	}
	if inv := r.URL.Query().Get("invoice"); inv != "" {
		v, err := s.CRM.Invoice(r.Context(), inv)
		if err != nil {
			return err
		}
		if err := s.apiInvoiceCheck(r, p, v); err != nil {
			return err
		}
		list, err := s.Pay.Payments(r.Context(), v.ID)
		if err == nil && list == nil {
			list = []paysys.Payment{}
		}
		if err == nil {
			writeJSON(w, http.StatusOK, list)
		}
		return err
	}
	list, err := s.Pay.Systems(r.Context())
	if err == nil && list == nil {
		list = []paysys.System{}
	}
	if err == nil {
		writeJSON(w, http.StatusOK, list)
	}
	return err
}

// apiInvoiceCheck: the key's user may read the deal or the lead of the invoice.
func (s *Server) apiInvoiceCheck(r *http.Request, p apikeys.Principal, v crm.Invoice) error {
	entity, id := v.Card()
	var item any
	var err error
	if entity == "lead" {
		item, err = s.CRM.Lead(r.Context(), id)
	} else {
		item, err = s.CRM.Deal(r.Context(), id)
	}
	if err != nil {
		return err
	}
	return s.CRM.CheckCard(r.Context(), p.UserID, entity, "read", id, crmResponsible(item))
}

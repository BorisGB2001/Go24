package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/sites"
)

// siteGoods fills the catalog blocks of a page with their products on sale (of the block's section, each with its
// variations). slug is the published site whose visitors see the pictures; "" — the editor and the preview, where
// they come from CRM.
func (s *Server) siteGoods(ctx context.Context, slug string, list []sites.Block) error {
	for i := range list {
		b := &list[i]
		if b.Type != "catalog" {
			continue
		}
		products, err := s.CRM.Catalog(ctx, "", b.Get("section"), "", false)
		if err != nil {
			return err
		}
		b.Goods = make([]sites.Good, 0, len(products))
		for _, p := range products {
			g := siteGood(slug, p)
			for _, v := range p.Variants {
				if v.Active {
					g.Variants = append(g.Variants, siteGood(slug, v))
				}
			}
			b.Goods = append(b.Goods, g)
		}
	}
	return nil
}

func siteGood(slug string, p crm.Product) sites.Good {
	g := sites.Good{ID: p.ID, Name: p.Name, Description: p.Description, Currency: p.Currency, Unit: p.Unit, Price: p.Price}
	switch {
	case p.PhotoKey == "":
	case slug == "":
		g.Photo = "/crm/products/" + p.ID + "/photo?v=" + p.PhotoKey
	default:
		g.Photo = "/site/" + slug + "/photo/" + p.ID + "?v=" + p.PhotoKey
	}
	return g
}

// siteProductPhoto is the picture of a product on sale, for the visitors of a published site.
func (s *Server) siteProductPhoto(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !s.Sites.Live(ctx, r.PathValue("slug")) {
		http.NotFound(w, r)
		return
	}
	p, err := s.CRM.Product(ctx, r.PathValue("id"))
	if err == nil && p.ParentID != "" { // a variation is on sale while its product is
		var o crm.Product
		if o, err = s.CRM.Product(ctx, p.ParentID); err == nil && !o.Active {
			p.Active = false
		}
	}
	if err != nil || !p.Active || p.PhotoKey == "" {
		http.NotFound(w, r)
		return
	}
	obj, err := s.Storages.Open(ctx, nil, p.PhotoKey)
	if err != nil {
		s.fail(w, "open product photo", err)
		return
	}
	defer obj.Close()
	w.Header().Set("Content-Type", p.PhotoType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'")
	w.Header().Set("Cache-Control", "public, max-age=86400") // the URL changes with every new picture
	_, _ = io.Copy(w, obj)
}

// siteOrder: a visitor orders the cart of a catalog block — a deal «Заказ с сайта» (source «Веб-сайт») in the block's
// pipeline with the products at the prices of the catalog, of the contact with the visitor's phone or e-mail (a new
// one if none), for the block's responsible, who gets a notification. A product no longer on sale in the block is
// refused, so the visitor sees the cart changed. ans are the visitor's answers, back — the page of the block.
func (s *Server) siteOrder(w http.ResponseWriter, r *http.Request, x sites.Site, p sites.Page, b sites.Block, ans map[string]string,
	back string) error {
	ctx := r.Context()
	cart, err := sites.ParseCart(r.FormValue("cart"))
	if err != nil {
		return err
	}
	if ans["phone"] == "" && ans["email"] == "" { // the order is to be confirmed
		return i18n.E("err.site_cart_contact")
	}
	products, err := s.CRM.Catalog(ctx, "", b.Get("section"), "", false)
	if err != nil {
		return err
	}
	sale := map[string]crm.Product{}
	for _, g := range products {
		if len(g.Variants) == 0 {
			sale[g.ID] = g
		}
		for _, v := range g.Variants {
			if v.Active {
				sale[v.ID] = v
			}
		}
	}
	rows := make([]crm.ProductRow, 0, len(cart))
	currency := ""
	for _, l := range cart {
		g, ok := sale[l.ID]
		if !ok {
			return i18n.E("err.site_cart_gone")
		}
		if currency == "" {
			currency = g.Currency
		}
		rows = append(rows, crm.ProductRow{ProductID: g.ID, Name: g.Name, Unit: g.Unit, Price: g.Price, Quantity: float64(l.Qty),
			VATRate: g.VATRate, VATIncluded: g.VATIncluded})
	}
	resp, err := s.Sites.Responsible(ctx, x, b)
	if err != nil {
		return err
	}
	who := ans["name"]
	for _, f := range []string{"phone", "email"} {
		if who == "" {
			who = ans[f]
		}
	}
	origin := crm.Dict{Code: "WEB"}
	contact := ""
	if ans["phone"] != "" || ans["email"] != "" {
		dups, err := s.CRM.Duplicates(ctx, "contact", "", "", ans["phone"], ans["email"])
		if err != nil {
			return err
		}
		for _, d := range dups {
			if d.Entity == "contact" && contact == "" {
				contact = d.ID
			}
		}
	}
	if contact == "" {
		if contact, err = s.CRM.SaveContact(ctx, resp, crm.Contact{Name: clipText(who, 300), Phone: ans["phone"], Email: ans["email"],
			Origin: origin, Responsible: crm.Ref{ID: resp}}); err != nil {
			return err
		}
	}
	company := ""
	if c, err := s.CRM.Contact(ctx, contact); err == nil {
		company = c.Company.ID
	}
	pipe := b.Get("pipeline")
	if list, err := s.CRM.Pipelines(ctx); err == nil && len(list) > 0 {
		found := false
		for _, pl := range list {
			found = found || pl.ID == pipe
		}
		if !found {
			pipe = list[0].ID
		}
	}
	title := s.portalTr("site.order_title", x.Name)
	if who != "" {
		title += " — " + who
	}
	var comment []string
	for _, f := range []string{"name", "phone", "email", "address", "comment"} {
		if v := ans[f]; v != "" {
			comment = append(comment, s.portalTr("site.form."+f)+": "+v)
		}
	}
	comment = append(comment, "", s.portalTr("site.lead_page", p.Title, strings.TrimRight(s.BaseURL, "/")+back))
	id, err := s.CRM.SaveDeal(ctx, resp, crm.Deal{Title: clipText(title, 300), PipelineID: pipe, Currency: currency,
		Comment: strings.Join(comment, "\n"), Origin: origin, Company: crm.Ref{ID: company}, Contacts: []crm.Ref{{ID: contact}},
		Responsible: crm.Ref{ID: resp}})
	if err != nil {
		return err
	}
	if _, err := s.CRM.SetProductRows(ctx, resp, "deal", id, rows); err != nil {
		return err
	}
	if err := s.setUTM(ctx, "deal", id, visitorUTM(r)); err != nil {
		s.Log.Error("site order utm", "deal", id, "err", err)
	}
	if err := s.Sites.NotifyLead(ctx, resp, title, "/crm/deals/"+id); err != nil {
		s.Log.Error("site order notify", "deal", id, "err", err)
	}
	return nil
}

// siteOrderErr tells a refusal of an order the visitor can mend (its locale key) from a failure.
func siteOrderErr(err error) string {
	var ie *i18n.Error
	if errors.As(err, &ie) && strings.HasPrefix(ie.Key, "err.site_") {
		return ie.Key
	}
	return ""
}

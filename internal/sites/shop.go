package sites

import (
	"context"
	"strconv"
	"strings"

	"go24/internal/i18n"
)

// ---------- the block «Каталог товаров»: the shop of a site ----------

// Good is a product of the CRM catalog as a catalog block shows it; a product with variations is chosen by them.
type Good struct {
	ID, Name, Description, Currency, Unit string
	Photo                                 string // its picture's address ("" — none)
	Price                                 float64
	Variants                              []Good
}

const (
	maxCartLines = 100
	maxQty       = 9999
)

// CartLine is a line of a visitor's order: a product of the catalog and how many.
type CartLine struct {
	ID  string
	Qty int
}

// ParseCart reads an order's cart — "id:qty" lines by commas, as the cart of the page sends it; the same product
// twice is added up. An empty cart, a strange line or too many of them are refused.
func ParseCart(v string) ([]CartLine, error) {
	var out []CartLine
	at := map[string]int{}
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item == "" {
			continue
		}
		id, q, _ := strings.Cut(item, ":")
		n, err := strconv.Atoi(q)
		if !uuidRe.MatchString(id) || err != nil || n < 1 || n > maxQty {
			return nil, i18n.E("err.site_cart")
		}
		if i, ok := at[id]; ok {
			out[i].Qty = min(out[i].Qty+n, maxQty)
			continue
		}
		at[id] = len(out)
		out = append(out, CartLine{id, n})
	}
	if len(out) == 0 || len(out) > maxCartLines {
		return nil, i18n.E("err.site_cart")
	}
	return out, nil
}

// Live reports whether a site is published (its products' pictures are open to everyone then).
func (s *Store) Live(ctx context.Context, slug string) bool {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sites WHERE slug = $1 AND published)`, slug).Scan(&ok)
	return err == nil && ok
}

package booking

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Услуги»: a resource provides services of the catalog (products of the type «Услуга»), each taking its
// own minutes; a booking keeps the services the client came for with the name and the price they were booked at, and
// lasts as long as they all take.

var ErrService = i18n.E("booking.err.service")

// Service is a service of a resource (its minutes) or of a booking (its name, price and minutes as booked).
type Service struct {
	ProductID, Name, Currency string
	Price                     float64
	Minutes                   int
}

// Total of the services and their currency (the first one's).
func Total(list []Service) (float64, string) {
	var sum float64
	cur := ""
	for _, x := range list {
		sum += x.Price
		if cur == "" {
			cur = x.Currency
		}
	}
	return sum, cur
}

// Minutes the services take together (0 — none).
func Minutes(list []Service) int {
	n := 0
	for _, x := range list {
		n += x.Minutes
	}
	return n
}

// Total of the booking's services.
func (b Booking) Total() (float64, string) { return Total(b.Services) }

// ServiceIDs: the catalog products of the booking's services.
func (b Booking) ServiceIDs() []string {
	ids := []string{}
	for _, x := range b.Services {
		if x.ProductID != "" {
			ids = append(ids, x.ProductID)
		}
	}
	return ids
}

// Keep: the services of the products ids for a booking that had old ones — a service it had keeps its price as
// booked, a new one is taken from the catalog when saved.
func Keep(ids []string, old []Service) []Service {
	list := []Service{}
	for _, id := range ids {
		x := Service{ProductID: id}
		if i := slices.IndexFunc(old, func(o Service) bool { return o.ProductID == id }); i >= 0 {
			x = old[i]
		}
		list = append(list, x)
	}
	return list
}

// Service of the resource by its product (false — the resource does not provide it).
func (r Resource) Service(productID string) (Service, bool) {
	i := slices.IndexFunc(r.Services, func(x Service) bool { return x.ProductID == productID })
	if i < 0 {
		return Service{}, false
	}
	return r.Services[i], true
}

// withServices fills the services of resources (a deleted or switched off product is not offered).
func (s *Store) withServices(ctx context.Context, list []Resource) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]string, len(list))
	for i, r := range list {
		ids[i] = r.ID
	}
	rows, err := s.db.Query(ctx, `SELECT rs.resource_id::text, p.id::text, p.name, p.price::float8, p.currency, rs.minutes
		FROM booking_resource_services rs JOIN crm_products p ON p.id = rs.product_id
		WHERE rs.resource_id::text = ANY($1) AND p.active ORDER BY rs.sort, lower(p.name)`, ids)
	if err != nil {
		return err
	}
	by := map[string][]Service{}
	var res string
	var x Service
	_, err = pgx.ForEachRow(rows, []any{&res, &x.ProductID, &x.Name, &x.Price, &x.Currency, &x.Minutes}, func() error {
		by[res] = append(by[res], x)
		return nil
	})
	for i := range list {
		list[i].Services = by[list[i].ID]
	}
	return err
}

// saveServices replaces the services of a resource: only the catalog's services, each once, minutes 5–1440 (0 — the
// resource's slot).
func saveServices(ctx context.Context, tx pgx.Tx, r Resource) error {
	if _, err := tx.Exec(ctx, `DELETE FROM booking_resource_services WHERE resource_id = $1`, r.ID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for i, x := range r.Services {
		if x.ProductID == "" || seen[x.ProductID] {
			continue
		}
		seen[x.ProductID] = true
		if x.Minutes == 0 {
			x.Minutes = r.Slot
		}
		if x.Minutes < 5 || x.Minutes > 1440 {
			return ErrSchedule
		}
		tag, err := tx.Exec(ctx, `INSERT INTO booking_resource_services (resource_id, product_id, minutes, sort)
			SELECT $1, p.id, $3, $4 FROM crm_products p WHERE p.id::text = $2 AND p.kind = 'service'`, r.ID, x.ProductID, x.Minutes, i)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrService
		}
	}
	return nil
}

// bookingServices fills the services of bookings.
func (s *Store) bookingServices(ctx context.Context, list []Booking) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]string, len(list))
	for i, b := range list {
		ids[i] = b.ID
	}
	rows, err := s.db.Query(ctx, `SELECT booking_id::text, coalesce(product_id::text, ''), name, price::float8, currency, minutes
		FROM booking_services WHERE booking_id::text = ANY($1) ORDER BY sort, id`, ids)
	if err != nil {
		return err
	}
	by := map[string][]Service{}
	var id string
	var x Service
	_, err = pgx.ForEachRow(rows, []any{&id, &x.ProductID, &x.Name, &x.Price, &x.Currency, &x.Minutes}, func() error {
		by[id] = append(by[id], x)
		return nil
	})
	for i := range list {
		list[i].Services = by[list[i].ID]
	}
	return err
}

// saveBookingServices replaces the services of a booking. A service given by its product alone (no name) is taken
// from the catalog as the resource provides it — its name, price and the resource's minutes for it; one with a name
// is kept as it is (a booking saved again keeps its prices).
func saveBookingServices(ctx context.Context, tx pgx.Tx, b Booking) error {
	if _, err := tx.Exec(ctx, `DELETE FROM booking_services WHERE booking_id = $1`, b.ID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for i, x := range b.Services {
		if x.ProductID != "" && seen[x.ProductID] {
			continue
		}
		seen[x.ProductID] = true
		if x.Name == "" {
			if x.ProductID == "" {
				continue
			}
			err := tx.QueryRow(ctx, `SELECT p.name, p.price::float8, p.currency, coalesce(rs.minutes, 0)
				FROM crm_products p LEFT JOIN booking_resource_services rs ON rs.product_id = p.id AND rs.resource_id::text = $2
				WHERE p.id::text = $1 AND p.kind = 'service'`, x.ProductID, b.ResourceID).Scan(&x.Name, &x.Price, &x.Currency, &x.Minutes)
			if err == pgx.ErrNoRows {
				return ErrService
			}
			if err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO booking_services (booking_id, product_id, name, price, currency, minutes, sort)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`, b.ID, nullable(x.ProductID), x.Name, x.Price, x.Currency, x.Minutes, i); err != nil {
			return err
		}
	}
	return nil
}

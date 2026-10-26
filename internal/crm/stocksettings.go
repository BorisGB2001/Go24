package crm

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// StockSettings are the settings of «Складской учёт» (administrators): how a deal's products are
// reserved and whether a won deal ships its products by itself.
type StockSettings struct {
	AutoReserve bool `json:"auto_reserve"` // a deal's product row without a warehouse is reserved on the default one
	ReserveDays int  `json:"reserve_days"` // the reserve's term set by itself, days; 0 — while the deal is open
	ShipOnWin   bool `json:"ship_on_win"`  // a deal moved to a success stage conducts a «Реализация» of what isn't shipped
}

// StockSettings reads the settings (the zero ones before any are saved).
func (s *Store) StockSettings(ctx context.Context) (StockSettings, error) {
	var set StockSettings
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = 'stock_settings'`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return set, nil
	}
	if err == nil {
		err = json.Unmarshal(raw, &set)
	}
	return set, err
}

// SetStockSettings keeps the settings.
func (s *Store) SetStockSettings(ctx context.Context, userID string, set StockSettings) error {
	if set.ReserveDays < 0 || set.ReserveDays > 3650 {
		return i18n.E("err.stock_reserve_days")
	}
	raw, _ := json.Marshal(set)
	_, err := s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ('stock_settings', $1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`, raw, userID)
	return err
}

// reserveUntil is the term of a reserve set by itself: today plus the settings' days; nil — while the deal is open.
func (set StockSettings) reserveUntil() *time.Time {
	if set.ReserveDays <= 0 {
		return nil
	}
	y, m, d := time.Now().Date()
	t := time.Date(y, m, d+set.ReserveDays, 0, 0, 0, 0, time.UTC)
	return &t
}

// shipOnWin: a deal just won ships its products when the settings say so — a «Реализация» of what isn't shipped yet
// is made and conducted; when it can't be conducted (not enough on a warehouse) it stays a draft. Either way the
// deal's timeline tells ("ship_auto", "ship_draft": "id|number"); nothing to ship, no warehouses — nothing happens.
func (s *Store) shipOnWin(ctx context.Context, userID, dealID string) error {
	set, err := s.StockSettings(ctx)
	if err != nil || !set.ShipOnWin {
		return err
	}
	var whs bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_warehouses WHERE active)`).Scan(&whs); err != nil || !whs {
		return err
	}
	if left, err := s.Unshipped(ctx, dealID); err != nil || left <= 0 {
		return err
	}
	doc, err := s.NewShipment(ctx, userID, dealID)
	if err != nil {
		return err
	}
	var number int
	if err := s.db.QueryRow(ctx, `SELECT number FROM crm_stock_docs WHERE id::text = $1`, doc).Scan(&number); err != nil {
		return err
	}
	kind := "ship_auto"
	if s.ConductStockDoc(ctx, userID, doc) != nil {
		kind = "ship_draft"
	}
	return s.event(ctx, s.db, "deal", dealID, userID, kind, doc+"|"+strconv.Itoa(number))
}

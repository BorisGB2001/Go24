package sites

import (
	"context"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"time"

	"go24/internal/i18n"
)

// ---------- the block «CRM-форма» ----------

// FormFields are the fields a form may ask, in the order they are shown.
var FormFields = []string{"name", "phone", "email", "comment"}

// Asks is how the form asks a field: "" — not, on — asked, req — required.
func (b Block) Asks(field string) string { return b.Data["ask_"+field] }

// FormBlock finds a visible form block of a page.
func (p Page) FormBlock(id string) (Block, bool) {
	for _, b := range p.Blocks {
		if b.ID == id && b.Type == "form" && !b.Hidden {
			return b, true
		}
	}
	return Block{}, false
}

const (
	maxAnswer  = 300
	maxComment = 5000
)

// Answer checks a visitor's answers to a form: only the fields it asks, the required ones filled, at least one of
// them; the e-mail looks like an address.
func (b Block) Answer(get func(string) string) (map[string]string, error) {
	out := map[string]string{}
	for _, f := range FormFields {
		ask := b.Asks(f)
		if ask == "" {
			continue
		}
		v, max := strings.TrimSpace(strings.ReplaceAll(get(f), "\r\n", "\n")), maxAnswer
		if f == "comment" {
			max = maxComment
		} else {
			v = strings.ReplaceAll(v, "\n", " ")
		}
		if r := []rune(v); len(r) > max {
			v = string(r[:max])
		}
		if v == "" && ask == "req" {
			return nil, i18n.E("err.site_form_required")
		}
		if f == "email" && v != "" {
			if a, err := mail.ParseAddress(v); err != nil || a.Address != v {
				return nil, i18n.E("err.site_form_email")
			}
		}
		if v != "" {
			out[f] = v
		}
	}
	if len(out) == 0 {
		return nil, i18n.E("err.site_form_required")
	}
	return out, nil
}

// Responsible is who gets the form's leads: the employee the block names, else the site's creator, else the first
// employee — whoever of them is active.
func (s *Store) Responsible(ctx context.Context, site Site, b Block) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT id::text FROM users WHERE active AND NOT guest
		ORDER BY id::text = $1 DESC, id::text = $2 DESC, created_at LIMIT 1`, b.Get("responsible"), site.CreatedBy).Scan(&id)
	return id, err
}

// NotifyLead tells the responsible of a lead from a form: body — the lead's title, url — its card.
func (s *Store) NotifyLead(ctx context.Context, userID, body, url string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url) VALUES ($1, 'site_form', left($2, 300), $3)`,
		userID, body, url)
	return err
}

// ---------- spam ----------

// FormStamp is the hidden field «t» of a form: when the page was given out. A person needs a few seconds to fill a
// form; a robot posting at once or without the page fails Human.
func FormStamp(now time.Time) string { return strconv.FormatInt(now.Unix(), 10) }

// Human: the trap field (hidden from people) is empty and the form was filled in 3 s – 1 day.
func Human(trap, stamp string, now time.Time) bool {
	t, err := strconv.ParseInt(stamp, 10, 64)
	if trap != "" || err != nil {
		return false
	}
	d := now.Sub(time.Unix(t, 0))
	return d >= 3*time.Second && d <= 24*time.Hour
}

// Hits limits how often one address may send forms: Max in Per.
type Hits struct {
	Max int
	Per time.Duration
	mu  sync.Mutex
	at  map[string][]time.Time
}

// Allow counts a sending from the key and reports whether it is within the limit.
func (h *Hits) Allow(key string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.at == nil {
		h.at = map[string][]time.Time{}
	}
	if len(h.at) > 10000 { // forget stale addresses
		for k, list := range h.at {
			if now.Sub(list[len(list)-1]) > h.Per {
				delete(h.at, k)
			}
		}
	}
	var keep []time.Time
	for _, t := range h.at[key] {
		if now.Sub(t) < h.Per {
			keep = append(keep, t)
		}
	}
	if len(keep) >= h.Max {
		h.at[key] = keep
		return false
	}
	h.at[key] = append(keep, now)
	return true
}

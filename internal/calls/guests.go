package calls

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// Guests are the guest links of calls, as the guest entry: one of the call makes a link for people
// without an account — for an hour, a day or a week, with a password or without; a guest gives their name and, with
// the lobby on, waits until someone of the call lets them in. A link revoked or out of time lets nobody in.
type Guests struct{ DB *pgxpool.Pool }

// GuestLink is a guest link of a call.
type GuestLink struct {
	ID, Token, Room, Title, Query, CreatedBy string
	Password, Lobby                          bool
	Expires                                  time.Time
	hash                                     string
}

// Guest is someone who came by a guest link.
type Guest struct {
	ID, LinkID, Name, State string
	At                      time.Time
	secret                  string
}

// Guest states.
const (
	GuestWaiting  = "waiting"
	GuestAdmitted = "admitted"
	GuestDenied   = "denied"
)

// GuestIdentity is the guest's identity in the LiveKit room (the people of go24 have their user IDs).
func GuestIdentity(id string) string { return "guest-" + id }

// GuestLinkTTLs are the lifetimes one may give a link; the first — when none is chosen.
var GuestLinkTTLs = []time.Duration{24 * time.Hour, time.Hour, 7 * 24 * time.Hour}

// ErrGuestGone: no such link, or it is revoked or out of time.
var ErrGuestGone = errors.New("calls: guest link gone")

func randToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// CreateLink makes a guest link of the call (room — its LiveKit room, query — the call as /call?… gives it).
func (g *Guests) CreateLink(ctx context.Context, by, room, title, query, password string, lobby bool, ttl time.Duration) (GuestLink, error) {
	hash := ""
	if password != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return GuestLink{}, err
		}
		hash = string(h)
	}
	if ttl <= 0 || ttl > 7*24*time.Hour {
		ttl = GuestLinkTTLs[0]
	}
	var id string
	var expires time.Time
	token := randToken(18)
	err := g.DB.QueryRow(ctx, `INSERT INTO call_guest_links (token, lk_room, title, call_query, created_by, password_hash, lobby, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now() + $8 * interval '1 second') RETURNING id::text, expires_at`,
		token, room, title, query, by, hash, lobby, int(ttl.Seconds())).Scan(&id, &expires)
	return GuestLink{ID: id, Token: token, Room: room, Title: title, Query: query, CreatedBy: by, Password: hash != "",
		Lobby: lobby, Expires: expires, hash: hash}, err
}

const linkCols = `id::text, token, lk_room, title, call_query, created_by::text, password_hash, lobby, expires_at`

func scanLink(row pgx.Row) (GuestLink, error) {
	var l GuestLink
	err := row.Scan(&l.ID, &l.Token, &l.Room, &l.Title, &l.Query, &l.CreatedBy, &l.hash, &l.Lobby, &l.Expires)
	l.Password = l.hash != ""
	return l, err
}

// Links are the call's guest links that still let one in, the newest first.
func (g *Guests) Links(ctx context.Context, room string) ([]GuestLink, error) {
	rows, err := g.DB.Query(ctx, `SELECT `+linkCols+` FROM call_guest_links
		WHERE lk_room = $1 AND revoked_at IS NULL AND expires_at > now() ORDER BY created_at DESC`, room)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (GuestLink, error) { return scanLink(row) })
}

// Open is the link of the token while it lets one in.
func (g *Guests) Open(ctx context.Context, token string) (GuestLink, error) {
	l, err := scanLink(g.DB.QueryRow(ctx, `SELECT `+linkCols+` FROM call_guest_links
		WHERE token = $1 AND revoked_at IS NULL AND expires_at > now()`, token))
	if err == pgx.ErrNoRows {
		return l, ErrGuestGone
	}
	return l, err
}

// CheckPassword: the password of a link with one.
func (l GuestLink) CheckPassword(password string) bool {
	return !l.Password || bcrypt.CompareHashAndPassword([]byte(l.hash), []byte(password)) == nil
}

// Revoke switches the call's link off; it gives the guests it let in (to be put out of the call).
func (g *Guests) Revoke(ctx context.Context, room, id string) ([]string, error) {
	tag, err := g.DB.Exec(ctx, `UPDATE call_guest_links SET revoked_at = now() WHERE id::text = $1 AND lk_room = $2 AND revoked_at IS NULL`, id, room)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrGuestGone
	}
	rows, err := g.DB.Query(ctx, `SELECT id::text FROM call_guests WHERE link_id::text = $1 AND state = 'admitted'`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// Arrive counts the guest in: let in at once when the link has no lobby, else waiting. The secret is for the guest's
// browser to keep.
func (g *Guests) Arrive(ctx context.Context, l GuestLink, name string) (Guest, string, error) {
	state := GuestWaiting
	if !l.Lobby {
		state = GuestAdmitted
	}
	secret := randToken(24)
	gu := Guest{LinkID: l.ID, Name: name, State: state}
	err := g.DB.QueryRow(ctx, `INSERT INTO call_guests (link_id, name, secret, state) VALUES ($1, $2, $3, $4) RETURNING id::text, created_at`,
		l.ID, name, secret, state).Scan(&gu.ID, &gu.At)
	return gu, secret, err
}

// Get is the link's guest whose browser has the secret; their page has just asked.
func (g *Guests) Get(ctx context.Context, l GuestLink, id, secret string) (Guest, error) {
	gu := Guest{ID: id, LinkID: l.ID}
	err := g.DB.QueryRow(ctx, `UPDATE call_guests SET seen_at = now() WHERE id::text = $1 AND link_id::text = $2
		RETURNING name, state, secret, created_at`, id, l.ID).Scan(&gu.Name, &gu.State, &gu.secret, &gu.At)
	if err == pgx.ErrNoRows || err == nil && subtle.ConstantTimeCompare([]byte(gu.secret), []byte(secret)) != 1 {
		return Guest{}, ErrGuestGone
	}
	return gu, err
}

// LobbyFresh: a waiting guest whose page asked within it is still in the lobby.
const LobbyFresh = 30 * time.Second

// Waiting are the guests in the lobby of the call, the first come first.
func (g *Guests) Waiting(ctx context.Context, room string) ([]Guest, error) {
	rows, err := g.DB.Query(ctx, `SELECT gu.id::text, gu.link_id::text, gu.name, gu.state, gu.created_at FROM call_guests gu
		JOIN call_guest_links l ON l.id = gu.link_id
		WHERE l.lk_room = $1 AND l.revoked_at IS NULL AND l.expires_at > now() AND gu.state = 'waiting' AND gu.seen_at > now() - $2 * interval '1 second'
		ORDER BY gu.created_at`, room, int(LobbyFresh.Seconds()))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Guest, error) {
		var gu Guest
		err := row.Scan(&gu.ID, &gu.LinkID, &gu.Name, &gu.State, &gu.At)
		return gu, err
	})
}

// Decide lets the waiting guest of the call in (admit) or not; false — they were not waiting (someone else decided).
func (g *Guests) Decide(ctx context.Context, room, id, by string, admit bool) (bool, error) {
	state := GuestDenied
	if admit {
		state = GuestAdmitted
	}
	tag, err := g.DB.Exec(ctx, `UPDATE call_guests gu SET state = $3, decided_by = $4 FROM call_guest_links l
		WHERE gu.id::text = $1 AND l.id = gu.link_id AND l.lk_room = $2 AND gu.state = 'waiting'`, id, room, state, by)
	return err == nil && tag.RowsAffected() == 1, err
}

// Expel keeps the guest of the call out of it for good (the host put them out): their link lets them in no more.
func (g *Guests) Expel(ctx context.Context, room, id, by string) error {
	_, err := g.DB.Exec(ctx, `UPDATE call_guests gu SET state = $3, decided_by = $4 FROM call_guest_links l
		WHERE gu.id::text = $1 AND l.id = gu.link_id AND l.lk_room = $2`, id, room, GuestDenied, by)
	return err
}

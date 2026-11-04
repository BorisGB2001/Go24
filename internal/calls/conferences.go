package calls

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// Conferences are the «Видеоконференции»: a conference of its own with a title, its people and a link
// that lasts until the conference is deleted — an employee joins by it at once, a guest with a name (and the
// password). In «Трансляция» only the presenters speak and show; the others watch and write in the call's chat.
// The organizer and the presenters lead the conference.
type Conferences struct{ DB *pgxpool.Pool }

// Conference is a video conference.
type Conference struct {
	ID, Title, CreatedBy, Chat, Token string
	Members, Presenters               []string
	Broadcast, Password               bool
	CreatedAt                         time.Time
}

// ConferenceInput is what the form of a conference gives; Password nil keeps the one it has ("" — none).
type ConferenceInput struct {
	Title               string
	Members, Presenters []string
	Broadcast           bool
	Password            *string
}

// ConferenceRoom is the LiveKit room of the conference.
func ConferenceRoom(id string) string { return "conf-" + id }

// ConferenceQuery is the conference as /call?… gives it.
func ConferenceQuery(id string) string { return "conf=" + id }

// conferenceLinkTTL: the link lasts until the conference is deleted.
const conferenceLinkTTL = "100 years"

// ErrNoConference: no such conference.
var ErrNoConference = errors.New("calls: no such conference")

// ErrConferenceTitle: a conference needs a title.
var ErrConferenceTitle = errors.New("calls: a conference needs a title")

// Host: the person leads the conference — its organizer or one of the presenters of a broadcast.
func (c Conference) Host(userID string) bool {
	return c.CreatedBy == userID || c.Broadcast && slices.Contains(c.Presenters, userID)
}

// Speaks: the person may turn on the microphone, the camera and the screen — anyone but in a broadcast, where only
// its organizer and presenters do.
func (c Conference) Speaks(userID string) bool { return !c.Broadcast || c.Host(userID) }

// Invited: the person is the organizer or one of the conference's people.
func (c Conference) Invited(userID string) bool {
	return c.CreatedBy == userID || slices.Contains(c.Members, userID) || slices.Contains(c.Presenters, userID)
}

func (in *ConferenceInput) clean(by string) error {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return ErrConferenceTitle
	}
	if len([]rune(in.Title)) > 200 {
		in.Title = string([]rune(in.Title)[:200])
	}
	uniq := func(ids []string) []string {
		out := []string{}
		for _, id := range ids {
			if id = strings.TrimSpace(id); id != "" && id != by && !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
		return out
	}
	in.Members, in.Presenters = uniq(in.Members), uniq(in.Presenters)
	if !in.Broadcast {
		in.Presenters = []string{}
	}
	for _, p := range in.Presenters { // a presenter is one of the conference's people
		if !slices.Contains(in.Members, p) {
			in.Members = append(in.Members, p)
		}
	}
	return nil
}

func hashPassword(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	h, err := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	return string(h), err
}

// Create makes the conference and its link.
func (cs *Conferences) Create(ctx context.Context, by string, in ConferenceInput) (Conference, error) {
	if err := in.clean(by); err != nil {
		return Conference{}, err
	}
	hash := ""
	if in.Password != nil {
		h, err := hashPassword(*in.Password)
		if err != nil {
			return Conference{}, err
		}
		hash = h
	}
	tx, err := cs.DB.Begin(ctx)
	if err != nil {
		return Conference{}, err
	}
	defer tx.Rollback(ctx)
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO call_conferences (title, created_by, members, broadcast, presenters)
		VALUES ($1, $2, $3::uuid[], $4, $5::uuid[]) RETURNING id::text`, in.Title, by, in.Members, in.Broadcast, in.Presenters).Scan(&id); err != nil {
		return Conference{}, err
	}
	if _, err := tx.Exec(ctx, `WITH l AS (INSERT INTO call_guest_links (token, lk_room, title, call_query, created_by, password_hash, lobby, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, false, now() + interval '`+conferenceLinkTTL+`') RETURNING id)
		UPDATE call_conferences SET link_id = (SELECT id FROM l) WHERE id::text = $7`,
		randToken(18), ConferenceRoom(id), in.Title, ConferenceQuery(id), by, hash, id); err != nil {
		return Conference{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Conference{}, err
	}
	return cs.Get(ctx, id)
}

const confCols = `c.id::text, c.title, c.created_by::text, c.chat, coalesce(l.token, ''), c.members::text[], c.presenters::text[],
	c.broadcast, coalesce(l.password_hash, '') <> '', c.created_at`

func scanConference(row pgx.Row) (Conference, error) {
	var c Conference
	err := row.Scan(&c.ID, &c.Title, &c.CreatedBy, &c.Chat, &c.Token, &c.Members, &c.Presenters, &c.Broadcast, &c.Password, &c.CreatedAt)
	if err == pgx.ErrNoRows {
		err = ErrNoConference
	}
	return c, err
}

const confFrom = ` FROM call_conferences c LEFT JOIN call_guest_links l ON l.id = c.link_id AND l.revoked_at IS NULL`

// Get is the conference.
func (cs *Conferences) Get(ctx context.Context, id string) (Conference, error) {
	return scanConference(cs.DB.QueryRow(ctx, `SELECT `+confCols+confFrom+` WHERE c.id::text = $1`, id))
}

// ByToken is the conference of its link's token.
func (cs *Conferences) ByToken(ctx context.Context, token string) (Conference, error) {
	return scanConference(cs.DB.QueryRow(ctx, `SELECT `+confCols+confFrom+` WHERE l.token = $1`, token))
}

// List are the person's conferences — organized or invited to, the newest first.
func (cs *Conferences) List(ctx context.Context, userID string) ([]Conference, error) {
	rows, err := cs.DB.Query(ctx, `SELECT `+confCols+confFrom+`
		WHERE c.created_by::text = $1 OR $1::uuid = ANY(c.members) ORDER BY c.created_at DESC LIMIT 200`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Conference, error) { return scanConference(row) })
}

// Update changes the conference; it gives the people newly invited.
func (cs *Conferences) Update(ctx context.Context, id string, in ConferenceInput) ([]string, error) {
	old, err := cs.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := in.clean(old.CreatedBy); err != nil {
		return nil, err
	}
	tx, err := cs.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE call_conferences SET title = $2, members = $3::uuid[], broadcast = $4, presenters = $5::uuid[],
		updated_at = now() WHERE id::text = $1`, id, in.Title, in.Members, in.Broadcast, in.Presenters); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE call_guest_links l SET title = $2 FROM call_conferences c WHERE c.id::text = $1 AND l.id = c.link_id`, id, in.Title); err != nil {
		return nil, err
	}
	if in.Password != nil {
		hash, err := hashPassword(*in.Password)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE call_guest_links l SET password_hash = $2 FROM call_conferences c WHERE c.id::text = $1 AND l.id = c.link_id`, id, hash); err != nil {
			return nil, err
		}
	}
	added := []string{}
	for _, m := range in.Members {
		if !slices.Contains(old.Members, m) {
			added = append(added, m)
		}
	}
	return added, tx.Commit(ctx)
}

// SetChat keeps the conference's chat in the messenger.
func (cs *Conferences) SetChat(ctx context.Context, id, chat string) error {
	_, err := cs.DB.Exec(ctx, `UPDATE call_conferences SET chat = $2 WHERE id::text = $1`, id, chat)
	return err
}

// NewLink revokes the conference's link and gives it a new one (the old one lets nobody in any more); it gives the
// guests the old link let in (to be put out of the call).
func (cs *Conferences) NewLink(ctx context.Context, id string) ([]string, error) {
	tx, err := cs.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	guests, err := revokeConferenceLink(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `WITH l AS (INSERT INTO call_guest_links (token, lk_room, title, call_query, created_by, password_hash, lobby, expires_at)
			SELECT $1, lk_room, title, call_query, created_by, password_hash, false, now() + interval '`+conferenceLinkTTL+`'
			FROM call_guest_links WHERE id = (SELECT link_id FROM call_conferences WHERE id::text = $2) RETURNING id)
		UPDATE call_conferences SET link_id = (SELECT id FROM l) WHERE id::text = $2`, randToken(18), id); err != nil {
		return nil, err
	}
	return guests, tx.Commit(ctx)
}

func revokeConferenceLink(ctx context.Context, tx pgx.Tx, id string) ([]string, error) {
	var link *string
	if err := tx.QueryRow(ctx, `SELECT link_id::text FROM call_conferences WHERE id::text = $1 FOR UPDATE`, id).Scan(&link); err != nil {
		if err == pgx.ErrNoRows {
			err = ErrNoConference
		}
		return nil, err
	}
	if link == nil {
		return nil, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE call_guest_links SET revoked_at = now() WHERE id::text = $1 AND revoked_at IS NULL`, *link); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM call_guests WHERE link_id::text = $1 AND state = 'admitted'`, *link)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// Delete removes the conference; its link lets nobody in any more. It gives the guests the link let in.
func (cs *Conferences) Delete(ctx context.Context, id string) ([]string, error) {
	tx, err := cs.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	guests, err := revokeConferenceLink(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM call_conferences WHERE id::text = $1`, id); err != nil {
		return nil, err
	}
	return guests, tx.Commit(ctx)
}

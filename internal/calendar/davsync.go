package calendar

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// A calendar of a CalDAV server connected two ways, as «Подключить CalDAV / Яндекс / iCloud»: one of the
// person's own calendars that go24-worker keeps the same as the server's collection — what was added, changed or
// deleted there comes into go24, what was done in go24 goes there. A series goes as one resource with its changed
// occurrences (RECURRENCE-ID); when both sides changed a meeting, the server's version wins.

// DavEvery is how often go24-worker syncs a connected CalDAV calendar.
const DavEvery = 5 * time.Minute

// DavKey is the key the passwords of CalDAV servers are sealed with, derived from go24's own secret.
func DavKey(secret string) []byte {
	k := sha256.Sum256([]byte("go24 caldav passwords\x00" + secret))
	return k[:]
}

// DavAccount is how go24 reaches the server's collection of a connected calendar.
type DavAccount struct {
	URL, Login, Password string
}

func (s *Store) gcm() (cipher.AEAD, error) {
	if len(s.Key) == 0 {
		return nil, errors.New("calendar: no key for the passwords")
	}
	block, err := aes.NewCipher(s.Key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (s *Store) seal(plain string) ([]byte, error) {
	gcm, err := s.gcm()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(plain), nil), nil
}

func (s *Store) open(sealed []byte) (string, error) {
	gcm, err := s.gcm()
	if err != nil {
		return "", err
	}
	if len(sealed) < gcm.NonceSize() {
		return "", errors.New("calendar: bad secret")
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
	return string(plain), err
}

// AddDav connects the server's collection a (its URL as the server named it) as a new calendar of the viewer's;
// returns its ID. The same collection is connected once.
func (s *Store) AddDav(ctx context.Context, viewer, name, color string, a DavAccount) (string, error) {
	var dup bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_calendars WHERE owner_id::text = $1 AND dav_url = $2)`, viewer, a.URL).Scan(&dup); err != nil {
		return "", err
	}
	if dup {
		return "", i18n.E("err.calendar_dav_dup")
	}
	secret, err := s.seal(a.Password)
	if err != nil {
		return "", err
	}
	if name = strings.TrimSpace(name); len([]rune(name)) > 100 {
		name = string([]rune(name)[:100])
	}
	if color = strings.ToLower(color); !colorRe.MatchString(color) {
		color = ""
	}
	id, err := s.SaveSection(ctx, viewer, Section{Name: name, Color: color})
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(ctx, `UPDATE user_calendars SET dav_url = $2, dav_login = $3, dav_secret = $4 WHERE id::text = $1`, id, a.URL, a.Login, secret)
	return id, err
}

// Dav is the viewer's connected CalDAV calendar id.
func (s *Store) Dav(ctx context.Context, viewer, id string) (Section, error) {
	list, err := s.Sections(ctx, viewer)
	if err != nil {
		return Section{}, err
	}
	for _, x := range list {
		if x.ID == id && x.DAV != "" {
			return x, nil
		}
	}
	return Section{}, ErrNotFound
}

// DavAccountOf is how go24 reaches the server of the connected calendar id.
func (s *Store) DavAccountOf(ctx context.Context, id string) (DavAccount, error) {
	var a DavAccount
	var secret []byte
	err := s.db.QueryRow(ctx, `SELECT dav_url, dav_login, dav_secret FROM user_calendars WHERE id::text = $1 AND dav_url <> ''`, id).
		Scan(&a.URL, &a.Login, &secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	a.Password, err = s.open(secret)
	return a, err
}

// SetDavPassword changes the login and password of the viewer's connected calendar id (the server changed them).
func (s *Store) SetDavPassword(ctx context.Context, viewer, id, login, password string) error {
	secret, err := s.seal(password)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE user_calendars SET dav_login = $3, dav_secret = $4, synced_at = NULL
		WHERE id::text = $1 AND owner_id::text = $2 AND dav_url <> ''`, id, viewer, strings.TrimSpace(login), secret)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// DavDue are the connected CalDAV calendars not synced for DavEvery or with meetings changed since, the longest
// waiting first.
func (s *Store) DavDue(ctx context.Context, now time.Time) ([]Section, error) {
	rows, err := s.db.Query(ctx, `SELECT c.id::text, c.name, c.color, c.source_url, c.synced_at, c.sync_error, c.owner_id::text, c.dav_url, c.dav_login
		FROM user_calendars c JOIN users u ON u.id = c.owner_id
		WHERE c.dav_url <> '' AND u.active AND (c.synced_at IS NULL OR c.synced_at < $1 OR c.dav_dirty)
		ORDER BY c.synced_at NULLS FIRST LIMIT 50`, now.Add(-DavEvery))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Section])
}

// DavBegin: the sync of the calendar begins — what is changed from now on is synced by the next one.
func (s *Store) DavBegin(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `UPDATE user_calendars SET dav_dirty = false WHERE id::text = $1`, id)
	return err
}

// DavState is what the last sync of the calendar left of each resource of the server's collection (by name).
type DavState struct{ ETag, Tag string }

func (s *Store) DavStates(ctx context.Context, id string) (map[string]DavState, error) {
	rows, err := s.db.Query(ctx, `SELECT name, etag, tag FROM calendar_dav_items WHERE calendar_id::text = $1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]DavState{}
	for rows.Next() {
		var name string
		var st DavState
		if err := rows.Scan(&name, &st.ETag, &st.Tag); err != nil {
			return nil, err
		}
		out[name] = st
	}
	return out, rows.Err()
}

// DavKeep remembers the resource name as both sides have it now.
func (s *Store) DavKeep(ctx context.Context, id, name string, st DavState) error {
	_, err := s.db.Exec(ctx, `INSERT INTO calendar_dav_items (calendar_id, name, etag, tag) VALUES ($1, $2, $3, $4)
		ON CONFLICT (calendar_id, name) DO UPDATE SET etag = $3, tag = $4`, id, name, st.ETag, st.Tag)
	return err
}

// DavForget: the resource name is gone from both sides.
func (s *Store) DavForget(ctx context.Context, id, name string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM calendar_dav_items WHERE calendar_id::text = $1 AND name = $2`, id, name)
	return err
}

// DavSynced keeps how the sync of the calendar went ("" — well).
func (s *Store) DavSynced(ctx context.Context, id, why string) error {
	_, err := s.db.Exec(ctx, `UPDATE user_calendars SET synced_at = now(), sync_error = $2 WHERE id::text = $1`, id, why)
	return err
}

// DavResources are the meetings of the viewer's collection (see DavEvents) as calendar object resources: a series
// with its changed occurrences (RECURRENCE-ID) is one resource, each by its name — those go24's CalDAV serves and
// those a connected calendar keeps on its server.
func (s *Store) DavResources(ctx context.Context, viewer, calendarID string) ([]DavItem, error) {
	list, err := s.DavEvents(ctx, viewer, calendarID)
	if err != nil {
		return nil, err
	}
	series := map[string]int{}
	for i, it := range list {
		if it.Event.Repeat != "" {
			series[it.Event.ID] = i
		}
	}
	children := map[string][]Event{}
	out := make([]DavItem, 0, len(list))
	for _, it := range list {
		if _, ok := series[it.Event.SeriesID]; ok && it.Event.SeriesAt != nil {
			children[it.Event.SeriesID] = append(children[it.Event.SeriesID], it.Event)
			continue
		}
		out = append(out, it)
	}
	for i, it := range out {
		if kids := children[it.Event.ID]; len(kids) > 0 {
			out[i].ICS = seriesICS(it.Event, kids, it.ICS)
			sum := sha256.Sum256([]byte(out[i].ICS))
			out[i].ETag = `"` + hex.EncodeToString(sum[:10]) + `"`
		}
	}
	return out, nil
}

// seriesICS is the calendar file of a series with its changed occurrences: they are not skipped (EXDATE) but told
// with the series' UID and their original starts. ics is the series alone (its DTSTAMP is kept).
func seriesICS(master Event, kids []Event, ics string) string {
	stamp := time.Now().UTC().Format("20060102T150405Z")
	if i := strings.Index(ics, "\r\nDTSTAMP:"); i >= 0 && len(ics) >= i+26 {
		stamp = ics[i+10 : i+26]
	}
	uid := master.UID
	if uid == "" {
		uid = master.ID + "@go24"
	}
	m := master
	m.Skip = slices.DeleteFunc(slices.Clone(master.Skip), func(t time.Time) bool {
		return slices.ContainsFunc(kids, func(k Event) bool { return k.SeriesAt.Equal(t) })
	})
	var b strings.Builder
	line := func(l string) { foldLine(&b, l) }
	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:-//go24//calendar//RU")
	line("CALSCALE:GREGORIAN")
	writeEvent(line, m, stamp)
	for _, k := range kids {
		k.UID, k.Repeat, k.Skip = uid, "", nil
		at := *k.SeriesAt
		writeEvent(func(l string) {
			line(l)
			if strings.HasPrefix(l, "UID:") {
				if master.AllDay {
					line("RECURRENCE-ID;VALUE=DATE:" + at.In(time.Local).Format("20060102"))
				} else {
					line("RECURRENCE-ID:" + at.UTC().Format("20060102T150405Z"))
				}
			}
		}, k, stamp)
	}
	line("END:VCALENDAR")
	return b.String()
}

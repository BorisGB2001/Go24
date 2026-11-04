package crm

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// «Переход по ссылке» (a trigger): the trigger names an address; in the letters robots write to the card's
// client the address becomes a tracked link of the card, and the client's following it moves the card on.

// maxLinkURL is how long the address of a link trigger may be.
const maxLinkURL = 2000

// linkInText is an address in a letter's text; the punctuation after it is the sentence's.
var linkInText = regexp.MustCompile(`https?://[^\s<>"']+`)

// linkURL is the trigger's address made tidy, "" — not an address of the web.
func linkURL(s string) string {
	s = strings.TrimSpace(s)
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(s) > maxLinkURL {
		return ""
	}
	return s
}

// trackLinks puts the card's tracked links in place of the addresses of «Переход по ссылке» triggers of its kind in
// the text the robot writes (author — whose robot it is); other addresses stay as they are.
func (s *Store) trackLinks(ctx context.Context, author string, c card, text string) (string, error) {
	if s.LinkURL == nil || (c.entity != "deal" && c.entity != "lead") {
		return text, nil
	}
	rows, err := s.db.Query(ctx, `SELECT DISTINCT t.params->>'url' FROM crm_triggers t JOIN crm_stages st ON st.id = t.stage_id
		WHERE t.kind = 'link' AND st.entity = $1`, c.entity)
	if err != nil {
		return text, err
	}
	urls, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(urls) == 0 {
		return text, err
	}
	tracked := map[string]bool{}
	for _, u := range urls {
		tracked[u] = true
	}
	var failed error
	text = linkInText.ReplaceAllStringFunc(text, func(m string) string {
		u := strings.TrimRight(m, ".,;:!?)]")
		if !tracked[u] || failed != nil {
			return m
		}
		var token string
		if failed = s.db.QueryRow(ctx, `INSERT INTO crm_trigger_links (token, entity, entity_id, url, author_id) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (entity, entity_id, url) DO UPDATE SET url = EXCLUDED.url RETURNING token`,
			formToken(), c.entity, c.id, u, author).Scan(&token); failed != nil {
			return m
		}
		return s.LinkURL(token) + m[len(u):]
	})
	return text, failed
}

// FollowLink is the client's following a tracked link: the first time the card's history says so; every time the
// card's «Переход по ссылке» triggers of the address fire (the card only goes forward anyway). It says where the
// client goes on to; an unknown link — ErrNotFound.
func (s *Store) FollowLink(ctx context.Context, token string) (string, error) {
	var entity, id, u, author string
	var first bool
	err := s.db.QueryRow(ctx, `UPDATE crm_trigger_links SET clicks = clicks + 1, clicked_at = coalesce(clicked_at, now())
		WHERE token = $1 RETURNING entity, entity_id::text, url, author_id::text, clicks = 1`, token).Scan(&entity, &id, &u, &author, &first)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if first {
		if err := s.event(ctx, s.db, entity, id, author, "link_clicked", u); err != nil {
			return u, err
		}
	}
	if err := s.fire(ctx, entity, id, "link", func(p TriggerParams) bool { return p.URL == u }); err != nil && !errors.Is(err, ErrNotFound) {
		return u, err
	}
	return u, nil
}

// LinkPeek is where a tracked link leads, without counting it (a link's preview, HEAD); unknown — ErrNotFound.
func (s *Store) LinkPeek(ctx context.Context, token string) (string, error) {
	var u string
	err := s.db.QueryRow(ctx, `SELECT url FROM crm_trigger_links WHERE token = $1`, token).Scan(&u)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return u, err
}

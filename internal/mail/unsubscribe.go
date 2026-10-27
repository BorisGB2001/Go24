package mail

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"syscall"
	"time"

	"go24/internal/i18n"
)

// «Отказаться от подписки», as in Outlook: a newsletter says how to leave it in its List-Unsubscribe header (RFC 2369)
// — a page, a letter to send (mailto:), or both; List-Unsubscribe-Post (RFC 8058) lets one leave it in one click,
// a POST to the page. go24 takes the way Outlook does: one click, else the letter, else the page the user opens.

// unsubWays are the mailto: and the web address of a List-Unsubscribe header (each the first of its kind).
func unsubWays(header string) (mailto *url.URL, web string) {
	for _, item := range strings.Split(header, ",") {
		item = strings.TrimSpace(item)
		if !strings.HasPrefix(item, "<") || !strings.HasSuffix(item, ">") {
			continue
		}
		u, err := url.Parse(strings.TrimSpace(item[1 : len(item)-1]))
		if err != nil {
			continue
		}
		switch strings.ToLower(u.Scheme) {
		case "mailto":
			if mailto == nil && u.Opaque != "" {
				mailto = u
			}
		case "https", "http":
			if web == "" && u.Host != "" {
				web = u.String()
			}
		}
	}
	return mailto, web
}

// publicOnly: the one-click request goes to addresses of the internet only, never to go24's own network.
func publicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return fmt.Errorf("%s is not an address of the internet", host)
	}
	return nil
}

// unsubClient sends the one-click requests (tests let it reach their own server).
var unsubClient = &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{
	DialContext: (&net.Dialer{Timeout: 10 * time.Second, Control: publicOnly}).DialContext, TLSHandshakeTimeout: 10 * time.Second,
}}

// Unsubscribe leaves the newsletter of a letter of the user: a one-click request or a letter from the mailbox; when
// the newsletter has only a page, open is it — the user leaves there. The letter keeps when it was done.
func (s *Store) Unsubscribe(ctx context.Context, userID, letterID string) (open string, err error) {
	m, err := s.message(ctx, userID, letterID)
	if err != nil {
		return "", err
	}
	mailto, web, oneClick, err := s.unsubOf(ctx, userID, m.ID)
	if err != nil {
		return "", err
	}
	switch wayOf(mailto, web, oneClick) {
	case UnsubClick:
		err = postOneClick(ctx, web)
	case UnsubMail:
		err = s.unsubLetter(ctx, m, mailto)
	case UnsubPage:
		open = web
	default:
		return "", i18n.E("err.mail_unsub_none")
	}
	if err != nil {
		return "", i18n.E("err.mail_unsub", err.Error())
	}
	_, err = s.db.Exec(ctx, `UPDATE mail_messages SET unsubscribed_at = now() WHERE id::text = $1`, m.ID)
	return open, err
}

// postOneClick is the RFC 8058 request: a POST of List-Unsubscribe=One-Click to the newsletter's page.
func postOneClick(ctx context.Context, page string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, page, strings.NewReader("List-Unsubscribe=One-Click"))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "go24")
	resp, err := unsubClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return errors.New(resp.Status)
	}
	return nil
}

// unsubLetter sends the letter a mailto: of List-Unsubscribe asks for, from the mailbox the newsletter came to.
func (s *Store) unsubLetter(ctx context.Context, m Message, mailto *url.URL) error {
	a, err := s.account(ctx, m.MailboxID)
	if err != nil {
		return err
	}
	addr, err := url.PathUnescape(mailto.Opaque)
	if err != nil {
		return err
	}
	to, err := mail.ParseAddress(addr)
	if err != nil {
		return err
	}
	q := mailto.Query()
	subject, text := q.Get("subject"), q.Get("body")
	if subject == "" {
		subject = "unsubscribe"
	}
	if text == "" {
		text = "unsubscribe"
	}
	e := envelope{From: mail.Address{Address: a.Email}, To: []*mail.Address{to}, Host: a.Email[strings.LastIndex(a.Email, "@")+1:],
		Now: time.Now(), Auto: "auto-generated"}
	msg, _ := compose(e, Letter{Subject: subject, Text: text})
	return s.sendSMTP(ctx, a, []string{to.Address}, msg, false)
}

// Ways of leaving a newsletter, as UnsubscribeWay tells them.
const (
	UnsubClick = "click" // one click: go24 asks the newsletter itself
	UnsubMail  = "mail"  // go24 sends the letter it asks for
	UnsubPage  = "page"  // the user leaves on the newsletter's page
)

// UnsubscribeWay is how Unsubscribe will leave the newsletter of a letter of the user ("" — it is none), page — the
// page the user opens for UnsubPage.
func (s *Store) UnsubscribeWay(ctx context.Context, userID, letterID string) (way, page string, err error) {
	mailto, web, oneClick, err := s.unsubOf(ctx, userID, letterID)
	if way = wayOf(mailto, web, oneClick); way == UnsubPage {
		page = web
	}
	return way, page, err
}

// unsubOf are the ways of leaving the newsletter of a letter of the user.
func (s *Store) unsubOf(ctx context.Context, userID, letterID string) (mailto *url.URL, web string, oneClick bool, err error) {
	var header string
	err = s.db.QueryRow(ctx, `SELECT x.unsubscribe, x.unsubscribe_one_click FROM mail_messages x JOIN mailboxes m ON m.id = x.mailbox_id
		WHERE x.id::text = $1 AND m.user_id::text = $2`, letterID, userID).Scan(&header, &oneClick)
	mailto, web = unsubWays(header)
	return mailto, web, oneClick, err
}

// wayOf is the way Outlook takes: one click, else the letter, else the page.
func wayOf(mailto *url.URL, web string, oneClick bool) string {
	switch {
	case web != "" && oneClick:
		return UnsubClick
	case mailto != nil:
		return UnsubMail
	case web != "":
		return UnsubPage
	}
	return ""
}

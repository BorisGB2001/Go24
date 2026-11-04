package notifyout

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"net/textproto"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// Email: notifications by e-mail «По e-mail» column of the notification settings. The administrators
// set the company's mail server — its address smtps://login@host[:465] or smtp://login@host[:587] (STARTTLS), the
// sender ?from=… when the login is not an address — and the password as the token. The employee needs no chat with a
// bot: «Подключить» takes their work address (Direct), and a letter goes only for a notification still unread in go24
// after Delay mails what was not seen on the portal.
type Email struct {
	Timeout time.Duration
	TLS     *tls.Config // nil — the system's roots (a field for tests)
}

func init() { Register(&Email{Timeout: 20 * time.Second}) }

func (e *Email) Name() string                 { return "email" }
func (e *Email) NeedsServer() bool            { return true }
func (e *Email) LinkURL(Bot, string) string   { return "" }
func (e *Email) Direct()                      {}
func (e *Email) Delay() time.Duration         { return 10 * time.Minute }
func (e *Email) CheckServer(raw string) error { _, err := parseSMTP(raw); return err }

// Updates: nobody writes to a mail server's «bot».
func (e *Email) Updates(_ context.Context, _ Bot, next int64) ([]Update, int64, error) {
	return nil, next, nil
}

// smtpServer is the parsed address of the company's mail server.
type smtpServer struct {
	host, port, login, from string
	implicit                bool // smtps: TLS from the start; smtp — STARTTLS when the server offers it
}

var errSMTPAddr = errors.New("notifyout: not an smtp:// or smtps:// address")

// parseSMTP reads smtps://login@host:port?from=sender; the login may be an address itself (notify@company.ru), so the
// last «@» parts it from the host.
func parseSMTP(raw string) (smtpServer, error) {
	var s smtpServer
	rest, ok := strings.CutPrefix(raw, "smtps://")
	if s.implicit, s.port = ok, "465"; !ok {
		if rest, ok = strings.CutPrefix(raw, "smtp://"); !ok {
			return s, errSMTPAddr
		}
		s.port = "587"
	}
	rest, query, _ := strings.Cut(rest, "?")
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		login, err := url.PathUnescape(rest[:i])
		if err != nil {
			return s, errSMTPAddr
		}
		s.login, rest = login, rest[i+1:]
	}
	s.host = strings.TrimRight(rest, "/")
	if h, p, err := net.SplitHostPort(s.host); err == nil {
		s.host, s.port = h, p
	}
	if s.host == "" || strings.ContainsAny(s.host, "/ ") {
		return s, errSMTPAddr
	}
	q, err := url.ParseQuery(query)
	if err != nil {
		return s, errSMTPAddr
	}
	if s.from = q.Get("from"); s.from == "" && strings.Contains(s.login, "@") {
		s.from = s.login
	}
	if !strings.Contains(s.from, "@") || strings.ContainsAny(s.from, "<>\r\n ") {
		return s, errSMTPAddr
	}
	return s, nil
}

// dial opens a session with the server, logged in.
func (e *Email) dial(ctx context.Context, b Bot) (*smtp.Client, smtpServer, error) {
	srv, err := parseSMTP(b.Server)
	if err != nil {
		return nil, srv, err
	}
	d := &net.Dialer{Timeout: e.Timeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(srv.host, srv.port))
	if err != nil {
		return nil, srv, err
	}
	_ = conn.SetDeadline(time.Now().Add(e.Timeout))
	cfg := &tls.Config{ServerName: srv.host, MinVersion: tls.VersionTLS12}
	if e.TLS != nil {
		cfg = e.TLS.Clone()
	}
	if srv.implicit {
		conn = tls.Client(conn, cfg)
	}
	c, err := smtp.NewClient(conn, srv.host)
	if err != nil {
		conn.Close()
		return nil, srv, err
	}
	if ok, _ := c.Extension("STARTTLS"); ok && !srv.implicit {
		if err := c.StartTLS(cfg); err != nil {
			c.Close()
			return nil, srv, err
		}
	}
	if srv.login != "" {
		// PlainAuth itself refuses to send the password unencrypted, but to the machine itself
		if err := c.Auth(smtp.PlainAuth("", srv.login, b.Token(), srv.host)); err != nil {
			c.Close()
			return nil, srv, err
		}
	}
	return c, srv, nil
}

// Check logs in to the server; the «bot's name» is the sender's address.
func (e *Email) Check(ctx context.Context, b Bot) (string, error) {
	c, srv, err := e.dial(ctx, b)
	if err != nil {
		return "", err
	}
	defer c.Close()
	return srv.from, c.Quit()
}

// Send mails the notification to the address (chat): its line as the subject, the line and its link as the text.
// The server refusing the address itself (5xx to RCPT) is ErrGone.
func (e *Email) Send(ctx context.Context, b Bot, chat string, m Message) error {
	c, srv, err := e.dial(ctx, b)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Mail(srv.from); err != nil {
		return err
	}
	if err := c.Rcpt(chat); err != nil {
		var te *textproto.Error
		if errors.As(err, &te) && te.Code >= 550 && te.Code <= 553 {
			return ErrGone
		}
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(letter(srv.from, chat, m, time.Now())); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// letter is the message: plain UTF-8 text, base64 lines of 76.
func letter(from, to string, m Message, now time.Time) []byte {
	subject, _, _ := strings.Cut(m.Text, "\n")
	if utf8.RuneCountInString(subject) > 120 {
		subject = string([]rune(subject)[:119]) + "…"
	}
	text := m.Text
	if m.Link != "" {
		text += "\n\n" + m.Link
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: go24 <%s>\r\nTo: <%s>\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%d.notify@go24>\r\n",
		from, to, mime.QEncoding.Encode("utf-8", subject), now.Format(time.RFC1123Z), now.UnixNano())
	buf.WriteString("Auto-Submitted: auto-generated\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(text, "\n", "\r\n")))
	for len(enc) > 76 {
		buf.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	buf.WriteString(enc + "\r\n")
	return buf.Bytes()
}

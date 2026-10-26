package mail

import (
	"context"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Заблокированные отправители» and «Надёжные отправители», as Outlook's «Нежелательная почта»: a blocked sender's
// letters go to «Нежелательная почта» — those in the inbox when blocked, and every new one before the rules see it;
// a safe sender is never blocked (an address safe within a blocked domain) and their pictures from elsewhere load at
// once. A sender is an address or a whole domain (@example.com).

const (
	SenderBlocked = "blocked"
	SenderSafe    = "safe"
)

// Sender is a blocked or safe sender of the user.
type Sender struct {
	Sender, Kind string
	CreatedAt    time.Time
}

// Domain: a whole domain rather than one address.
func (s Sender) Domain() bool { return strings.HasPrefix(s.Sender, "@") }

// NormSender is an address or a domain as kept: «Name <a@b>» → a@b, «b.com» or «@b.com» → @b.com; "" — neither.
func NormSender(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if a, err := mail.ParseAddress(v); err == nil {
		v = a.Address
	}
	if v == "" || strings.ContainsAny(v, " ,;<>\"") || len(v) > 300 {
		return ""
	}
	at := strings.LastIndex(v, "@")
	domain := v[at+1:]
	if domain == "" || !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return ""
	}
	if at <= 0 { // a domain
		return "@" + domain
	}
	return v
}

// senderKeys are what a sender's address is listed as: itself, its domain and the domains above it.
func senderKeys(addr string) []string {
	addr = strings.ToLower(strings.TrimSpace(addr))
	at := strings.LastIndex(addr, "@")
	if at < 0 {
		return nil
	}
	keys := []string{addr}
	for d := addr[at+1:]; strings.Contains(d, "."); d = d[strings.Index(d, ".")+1:] {
		keys = append(keys, "@"+d)
	}
	return keys
}

// Senders are the user's blocked or safe senders, by address.
func (s *Store) Senders(ctx context.Context, userID, kind string) ([]Sender, error) {
	rows, err := s.db.Query(ctx, `SELECT sender, kind, created_at FROM mail_senders WHERE user_id::text = $1 AND kind = $2 ORDER BY sender`, userID, kind)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Sender])
}

// AddSender puts an address or a domain on the list (taking it off the other one); a blocked sender's letters in
// the inboxes go to «Нежелательная почта» at once — how many went.
func (s *Store) AddSender(ctx context.Context, userID, kind, sender string) (int, error) {
	sender = NormSender(sender)
	if sender == "" || (kind != SenderBlocked && kind != SenderSafe) {
		return 0, i18n.E("err.mail_sender")
	}
	if kind == SenderBlocked && s.ownAddress(ctx, userID, sender) {
		return 0, i18n.E("err.mail_block_own")
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO mail_senders (user_id, sender, kind) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, sender) DO UPDATE SET kind = EXCLUDED.kind, created_at = now()`, userID, sender, kind); err != nil {
		return 0, err
	}
	if kind != SenderBlocked {
		return 0, nil
	}
	return s.junkFrom(ctx, userID, sender)
}

// ownAddress: is the sender one of the user's own mailboxes? Outlook does not block oneself either.
func (s *Store) ownAddress(ctx context.Context, userID, sender string) bool {
	var own bool
	_ = s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mailboxes WHERE user_id::text = $1 AND lower(email) = $2)`, userID, sender).Scan(&own)
	return own
}

// DropSender takes an address or a domain off the user's lists.
func (s *Store) DropSender(ctx context.Context, userID, sender string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM mail_senders WHERE user_id::text = $1 AND sender = $2`, userID, strings.ToLower(strings.TrimSpace(sender)))
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// BlockSenders blocks the senders of letters (domain: their whole domains), as Outlook's «Заблокировать
// отправителя»: the letters themselves and the others of them in the inboxes go to «Нежелательная почта».
func (s *Store) BlockSenders(ctx context.Context, userID string, letterIDs []string, domain bool) ([]string, int, error) {
	from, err := s.lettersFrom(ctx, userID, letterIDs)
	if err != nil {
		return nil, 0, err
	}
	var blocked []string
	moved := 0
	for _, addr := range from {
		sender := addr
		if domain {
			sender = "@" + addr[strings.LastIndex(addr, "@")+1:]
		}
		n, err := s.AddSender(ctx, userID, SenderBlocked, sender)
		if err != nil {
			return blocked, moved, err
		}
		blocked, moved = append(blocked, NormSender(sender)), moved+n
	}
	return blocked, moved, nil
}

// TrustSenders makes the senders of letters safe («Всегда показывать содержимое от …»).
func (s *Store) TrustSenders(ctx context.Context, userID string, letterIDs []string) ([]string, error) {
	from, err := s.lettersFrom(ctx, userID, letterIDs)
	if err != nil {
		return nil, err
	}
	for _, addr := range from {
		if _, err := s.AddSender(ctx, userID, SenderSafe, addr); err != nil {
			return nil, err
		}
	}
	return from, nil
}

// lettersFrom are the senders of the user's letters (each once); none — ErrNotFound.
func (s *Store) lettersFrom(ctx context.Context, userID string, letterIDs []string) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT lower(x.from_addr) FROM mail_messages x JOIN mailboxes m ON m.id = x.mailbox_id
		WHERE x.id::text = ANY($1) AND m.user_id::text = $2 AND x.from_addr LIKE '%@%'`, letterIDs, userID)
	if err != nil {
		return nil, err
	}
	from, err := pgxCollectStrings(rows)
	if err == nil && len(from) == 0 {
		err = ErrNotFound
	}
	return from, err
}

// SenderKind is how the user lists the sender's address: blocked, safe or "" (safe wins over a blocked domain).
func (s *Store) SenderKind(ctx context.Context, userID, addr string) (string, error) {
	keys := senderKeys(addr)
	if len(keys) == 0 {
		return "", nil
	}
	var kind string
	err := s.db.QueryRow(ctx, `SELECT coalesce((SELECT kind FROM mail_senders WHERE user_id::text = $1 AND sender = ANY($2)
		ORDER BY kind = 'safe' DESC LIMIT 1), '')`, userID, keys).Scan(&kind)
	return kind, err
}

// blockedSQL: the letter x (of the user $1) is from a blocked sender, not a safe one.
var blockedSQL = `EXISTS (SELECT 1 FROM mail_senders b WHERE b.user_id::text = $1 AND b.kind = 'blocked' AND ` + senderIs("b.sender") + `)
	AND NOT EXISTS (SELECT 1 FROM mail_senders g WHERE g.user_id::text = $1 AND g.kind = 'safe' AND ` + senderIs("g.sender") + `)`

// senderIs: the letter x is from the sender (an expression): the address itself, or one of the domain or under it.
func senderIs(sender string) string {
	return `(` + sender + ` = lower(x.from_addr) OR (left(` + sender + `, 1) = '@' AND right(lower(x.from_addr), length(` + sender + `)) IN (` +
		sender + `, '.' || substr(` + sender + `, 2))))`
}

// junkFrom moves the letters of a blocked sender in the user's inboxes to «Нежелательная почта».
func (s *Store) junkFrom(ctx context.Context, userID, sender string) (int, error) {
	rows, err := s.db.Query(ctx, `SELECT x.id::text FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = x.mailbox_id
		WHERE m.user_id::text = $1 AND f.role = 'inbox' AND `+senderIs("$2::text")+` AND `+blockedSQL,
		userID, sender)
	if err != nil {
		return 0, err
	}
	ids, err := pgxCollectStrings(rows)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	return len(ids), s.Act(ctx, userID, ids, "junk", "")
}

// junkBlocked: the new letters from blocked senders go to «Нежелательная почта» before anything else is done with
// them (the automatic reply, the rules, the notifications); the rest are returned.
func (s *Store) junkBlocked(ctx context.Context, userID string, news []arrived) ([]arrived, error) {
	ids := make([]string, len(news))
	for i, n := range news {
		ids[i] = n.id
	}
	rows, err := s.db.Query(ctx, `SELECT x.id::text FROM mail_messages x WHERE x.id::text = ANY($2) AND `+blockedSQL, userID, ids)
	if err != nil {
		return news, err
	}
	junk, err := pgxCollectStrings(rows)
	if err != nil || len(junk) == 0 {
		return news, err
	}
	var rest []arrived
	for _, n := range news {
		if !contains(junk, n.id) {
			rest = append(rest, n)
		}
	}
	return rest, s.Act(ctx, userID, junk, "junk", "")
}

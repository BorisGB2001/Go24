// Package mail is «Почта»: an employee's mailboxes (IMAP to read the inbox, SMTP to send). go24 fetches new
// letters in the background and keeps their plain text; the password is kept sealed with go24's own key.
package mail

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
)

var (
	ErrNotFound  = i18n.E("err.not_found")
	ErrForbidden = i18n.E("err.forbidden")
)

type Store struct {
	db  *pgxpool.Pool
	key []byte // AES-256 key for the passwords
	// Log reports a background failure (nil — none)
	Log func(what, id string, err error)
	// Keep stores an attachment of a letter (the mailbox's owner, the letter's id); nil — attachments are not kept
	Keep func(ctx context.Context, owner, letterID string, f Attachment) error
	// Open reads a kept file back (a draft's attachments when it is sent); nil — they cannot be sent
	Open func(ctx context.Context, storageID *string, key string) (io.ReadCloser, error)
	// Tr is a text in go24's language for what go24 sends by itself (automatic replies, forwards); nil — the keys
	Tr func(key string, args ...any) string
	// Lead makes a CRM lead of a letter from an address the CRM does not know (the mailbox's owner); nil — none
	Lead func(ctx context.Context, owner string, l NewLead) (string, error)
}

// NewStore: secret is go24's own secret (the session secret); the mail key is derived from it.
func NewStore(db *pgxpool.Pool, secret string) *Store {
	k := sha256.Sum256([]byte("go24 mail passwords\x00" + secret))
	return &Store{db: db, key: k[:]}
}

func (s *Store) seal(plain string) ([]byte, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
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
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(sealed) < gcm.NonceSize() {
		return "", errors.New("mail: bad secret")
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
	return string(plain), err
}

// Mailbox is a connected mailbox (without its password).
type Mailbox struct {
	ID, UserID, Email, IMAPHost, SMTPHost, Login string
	IMAPPort, SMTPPort                           int
	TLS                                          bool
	SyncedAt                                     *time.Time
	Error                                        string
	Unseen                                       int
}

// Settings are what the form gives to connect a mailbox.
type Settings struct {
	Email, IMAPHost, SMTPHost, Login, Password string
	IMAPPort, SMTPPort                         int
	TLS                                        bool
}

// known are the servers of well-known mail services, by the address's domain (the form may leave them empty).
var known = map[string][2]string{
	"gmail.com": {"imap.gmail.com", "smtp.gmail.com"}, "googlemail.com": {"imap.gmail.com", "smtp.gmail.com"},
	"yandex.ru": {"imap.yandex.ru", "smtp.yandex.ru"}, "ya.ru": {"imap.yandex.ru", "smtp.yandex.ru"}, "yandex.com": {"imap.yandex.com", "smtp.yandex.com"},
	"mail.ru": {"imap.mail.ru", "smtp.mail.ru"}, "bk.ru": {"imap.mail.ru", "smtp.mail.ru"}, "list.ru": {"imap.mail.ru", "smtp.mail.ru"},
	"inbox.ru": {"imap.mail.ru", "smtp.mail.ru"}, "outlook.com": {"outlook.office365.com", "smtp.office365.com"},
	"hotmail.com": {"outlook.office365.com", "smtp.office365.com"},
}

// Preset fills the servers of a well-known service when the form leaves them empty.
func (st *Settings) Preset() {
	at := strings.LastIndex(st.Email, "@")
	if at < 0 {
		return
	}
	srv, ok := known[strings.ToLower(strings.TrimSpace(st.Email[at+1:]))]
	if !ok {
		return
	}
	if strings.TrimSpace(st.IMAPHost) == "" {
		st.IMAPHost, st.IMAPPort = srv[0], 993
	}
	if strings.TrimSpace(st.SMTPHost) == "" {
		st.SMTPHost, st.SMTPPort = srv[1], 465
		if srv[1] == "smtp.office365.com" {
			st.SMTPPort = 587 // Microsoft: STARTTLS only
		}
	}
}

func (st *Settings) check() error {
	st.Preset()
	st.Email, st.IMAPHost, st.SMTPHost, st.Login = strings.TrimSpace(st.Email), strings.TrimSpace(st.IMAPHost), strings.TrimSpace(st.SMTPHost), strings.TrimSpace(st.Login)
	st.Password = strings.ReplaceAll(st.Password, " ", "") // app passwords are shown in groups of four
	if a, err := mail.ParseAddress(st.Email); err != nil || a.Address != st.Email {
		return i18n.E("err.mail_email")
	}
	if st.IMAPHost == "" || st.SMTPHost == "" || st.IMAPPort < 1 || st.IMAPPort > 65535 || st.SMTPPort < 1 || st.SMTPPort > 65535 ||
		strings.ContainsAny(st.IMAPHost+st.SMTPHost, " /:") {
		return i18n.E("err.mail_server")
	}
	if st.Login == "" {
		st.Login = st.Email
	}
	if st.Password == "" {
		return i18n.E("err.mail_password")
	}
	return nil
}

// Add connects a mailbox of the user (go24 checks the IMAP login first: see Probe).
func (s *Store) Add(ctx context.Context, userID string, st Settings) (string, error) {
	if err := st.check(); err != nil {
		return "", err
	}
	secret, err := s.seal(st.Password)
	if err != nil {
		return "", err
	}
	var id string
	err = s.db.QueryRow(ctx, `INSERT INTO mailboxes (user_id, email, imap_host, imap_port, smtp_host, smtp_port, tls, login, secret)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`, userID, st.Email, st.IMAPHost, st.IMAPPort, st.SMTPHost, st.SMTPPort,
		st.TLS, st.Login, secret).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return "", i18n.E("err.mail_exists")
	}
	return id, err
}

const mailboxSQL = `SELECT m.id, m.user_id::text, m.email, m.imap_host, m.smtp_host, m.login, m.imap_port, m.smtp_port, m.tls,
		m.synced_at, m.error, (SELECT count(*) FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id
			WHERE f.mailbox_id = m.id AND f.role = 'inbox' AND NOT x.seen)::int
	FROM mailboxes m`

func scanMailboxes(rows pgx.Rows, err error) ([]Mailbox, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Mailbox])
}

// Mailboxes are the user's own.
func (s *Store) Mailboxes(ctx context.Context, userID string) ([]Mailbox, error) {
	return scanMailboxes(s.db.Query(ctx, mailboxSQL+` WHERE m.user_id = $1 ORDER BY m.email`, userID))
}

// Mailbox is one of the user's mailboxes.
func (s *Store) Mailbox(ctx context.Context, userID, id string) (Mailbox, error) {
	list, err := scanMailboxes(s.db.Query(ctx, mailboxSQL+` WHERE m.id::text = $1 AND m.user_id = $2`, id, userID))
	if err != nil {
		return Mailbox{}, err
	}
	if len(list) == 0 {
		return Mailbox{}, ErrNotFound
	}
	return list[0], nil
}

// Remove disconnects a mailbox of the user with the letters go24 keeps.
func (s *Store) Remove(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM mailboxes WHERE id::text = $1 AND user_id = $2`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// Message is a letter — or, in a list shown as conversations, a conversation: its newest letter in the folder, with
// Count letters (Unread of them unread) whose ids are IDs.
type Message struct {
	ID, MailboxID, MailboxEmail, FolderID, FolderName, FolderRole, FromAddr, FromName, To, CC, Subject, Body string
	MessageID, Thread                                                                                        string
	SentAt                                                                                                   time.Time
	Seen, Flagged, FlagDone, Pinned                                                                          bool
	FlagDue, SnoozeUntil                                                                                     *time.Time
	Categories                                                                                               []string
	LeadID                                                                                                   string
	Attached                                                                                                 int // files kept with the letter (the conversation)
	Count, Unread                                                                                            int
	IDs                                                                                                      []string
	HTML, BCC                                                                                                string // the HTML, cleaned ("" — a plain text letter; not in a list)
	Importance                                                                                               int    // -1 low, 0 normal, 1 high
	ReceiptTo                                                                                                string // an incoming letter asks for a read receipt to this address
	ReceiptDone                                                                                              bool
	Draft                                                                                                    bool       // go24's own draft (opens in the editor)
	SendAt                                                                                                   *time.Time // the draft goes then
	SendError                                                                                                string     // why it did not go
	Other                                                                                                    bool       // an inbox letter under «Другие»
	Answered                                                                                                 string     // "", reply, fwd: «Вы ответили», «Вы переслали»
	AnsweredAt                                                                                               *time.Time // when go24 sent the answer (nil — unknown: another program)
	FlagRemind                                                                                               *time.Time // the flag's reminder («Добавить напоминание»)
	Unsubscribe                                                                                              bool       // a newsletter one can leave («Отказаться от подписки»)
	Unsubscribed                                                                                             *time.Time // when the user left it
	Voting, Vote                                                                                             string     // the voting buttons it asks for («A;B»), the choice it votes for (voting.go)
	SMIMESign, SMIMESigner, SMIMEEnc                                                                         string     // its S/MIME signature and encryption (smime.go)
}

// From is how the sender is shown.
func (m Message) From() string {
	if m.FromName != "" {
		return m.FromName
	}
	return m.FromAddr
}

// Outgoing: a letter of «Отправленные» or «Черновики» — the list shows whom it is to.
func (m Message) Outgoing() bool { return m.FolderRole == "sent" || m.FolderRole == "drafts" }

// Snoozed: the letter is put off till later.
func (m Message) Snoozed() bool { return m.SnoozeUntil != nil && m.SnoozeUntil.After(time.Now()) }

// HasCategory: is the category given to the letter?
func (m Message) HasCategory(id string) bool {
	for _, c := range m.Categories {
		if c == id {
			return true
		}
	}
	return false
}

// messageCols are a letter's columns in the order of Message (up to LeadID); body is the column of its text
const messageCols = `x.id::text, x.mailbox_id::text, m.email, f.id::text, f.name, f.role, x.from_addr, x.from_name, x.to_addr, x.cc_addr,
	x.subject, %s, x.message_id, x.thread, x.sent_at, x.seen, x.flagged, x.flag_done, x.pinned, x.flag_due::timestamptz, x.snooze_until,
	x.categories::text[], coalesce(x.lead_id::text, ''), (SELECT count(*) FROM files fl WHERE fl.mail_id = x.id AND fl.content_id = '')::int`

// messageExtra are a letter's columns after IDs; html is the column of its HTML
const messageExtra = `, %s, x.bcc_addr, x.importance::int, x.receipt_to, x.receipt_done, d.id IS NOT NULL, d.send_at, coalesce(d.error, ''), x.other, x.answered, x.answered_at, x.flag_remind,
	x.unsubscribe <> '', x.unsubscribed_at, x.voting, x.vote, x.smime_sign, x.smime_signer, x.smime_enc`

const messageFrom = ` FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = f.mailbox_id
	LEFT JOIN mail_drafts d ON d.id = x.id`

var messageSQL = `SELECT ` + fmt.Sprintf(messageCols, "x.body") + `, 1, (NOT x.seen)::int, ARRAY[x.id::text]` + fmt.Sprintf(messageExtra, "x.body_html") + messageFrom

// The virtual folders: «Все входящие» (the inboxes of all the user's mailboxes), «С флажком», «Отложенные», a
// category's letters (CategoryFolder + its id) and a search folder (SearchFolder + the saved search's id).
const (
	AllInboxes     = "all"
	FlaggedFolder  = "flagged"
	SnoozedFolder  = "snoozed"
	CategoryFolder = "cat:"
)

// Virtual: is the folder one of the virtual ones?
func Virtual(folderID string) bool {
	return folderID == AllInboxes || folderID == FlaggedFolder || folderID == SnoozedFolder || strings.HasPrefix(folderID, CategoryFolder) ||
		strings.HasPrefix(folderID, SearchFolder)
}

// MaxList bounds a folder's letters shown at once.
const MaxList = 200

// previewLen: the list keeps this much of a letter's text for its preview.
const previewLen = 400

// Messages of a folder of the user's mailboxes (or a virtual one), newest first; q — a search (see ParseQuery).
func (s *Store) Messages(ctx context.Context, userID, folderID, q string) ([]Message, error) {
	return s.List(ctx, userID, folderID, q, false)
}

// List is a folder's letters, newest first (see ListBy).
func (s *Store) List(ctx context.Context, userID, folderID, q string, conv bool) ([]Message, error) {
	return s.ListBy(ctx, userID, ListOpts{Folder: folderID, Q: q, Conv: conv})
}

// The filters over the list, as Outlook's «Фильтр»: unread, flagged, to me (the mailbox is in «Кому»), with files,
// mentioning me (@ and the user's first name in the text), important.
var Filters = []string{"unread", "flagged", "tome", "attach", "mention", "high"}

// The orders of the list, as Outlook's «Сортировка»: by date, sender (whom to, in «Отправленные»), size, subject
// (without Re:/Fwd:), importance, flag; each has its usual direction (the newest, the biggest, A to Z).
var Sorts = []string{"date", "from", "size", "subject", "importance", "flag"}

// SortAsc: does the order go up unless turned?
func SortAsc(sort string) bool { return sort == "from" || sort == "subject" }

// ListOpts are what a list shows.
type ListOpts struct {
	Folder string
	Q      string // a search
	All    bool   // the search goes through all the user's folders
	Conv   bool   // one row per conversation
	View   string // ViewFocused or ViewOther: a tab of an inbox ("" — all its letters)
	Filter string // one of Filters ("" — none)
	Sort   string // one of Sorts ("" — date)
	Turn   bool   // the other way than the order's usual one
}

// reReSQL is what a subject starts with in a reply or a forward.
const reReSQL = `'^\s*((re|fwd?|aw|wg|отв|пересл|ответ)(\[\d+\])?\s*:\s*)+'`

// sizeExpr is a letter's size: its text and files.
const sizeExpr = `(length(x.body) + length(x.body_html) + coalesce((SELECT sum(fl.size) FROM files fl WHERE fl.mail_id = x.id), 0))`

// sortExpr are the orders' expressions: over the letters (x) and over the conversations (c, see ListBy).
var sortExpr = map[string][2]string{
	"date": {"coalesce(x.woke_at, x.sent_at)", "last_at"},
	"from": {"lower(CASE WHEN f.role IN ('sent', 'drafts') THEN x.to_addr ELSE coalesce(nullif(x.from_name, ''), x.from_addr) END)",
		"lower(CASE WHEN role IN ('sent', 'drafts') THEN to_addr ELSE coalesce(nullif(from_name, ''), from_addr) END)"},
	"size":       {sizeExpr, "size_all"},
	"subject":    {"lower(regexp_replace(x.subject, " + reReSQL + ", '', 'i'))", "lower(regexp_replace(subject, " + reReSQL + ", '', 'i'))"},
	"importance": {"x.importance", "importance"},
	"flag":       {"x.flagged", "flagged_any"},
}

// ListBy is a folder's letters, as Outlook shows them: the pinned ones on top, then the newest (a letter back from
// «Отложить» counts from then) or in the order asked; snoozed letters are only in «Отложенные». A search through
// all folders finds snoozed letters too; a search folder (SearchFolder + id) is its saved search through all of them.
func (s *Store) ListBy(ctx context.Context, userID string, o ListOpts) ([]Message, error) {
	if strings.HasPrefix(o.Folder, SearchFolder) {
		saved, err := s.SavedSearch(ctx, userID, o.Folder[len(SearchFolder):])
		if err != nil {
			return nil, err
		}
		o.Q, o.All = saved.Q, true
	}
	everywhere := o.All && strings.TrimSpace(o.Q) != ""
	args := sqlArgs{userID}
	var where string
	switch folderID := o.Folder; {
	case everywhere:
		where = "true"
	case folderID == AllInboxes:
		where = "f.role = 'inbox'"
	case folderID == FlaggedFolder:
		where = "x.flagged"
	case folderID == SnoozedFolder:
		where = "x.snooze_until > now()"
	case strings.HasPrefix(folderID, CategoryFolder):
		if _, err := s.Category(ctx, userID, folderID[len(CategoryFolder):]); err != nil {
			return nil, err
		}
		where = "x.categories @> ARRAY[" + args.add(folderID[len(CategoryFolder):]) + "::uuid] AND f.role NOT IN ('trash', 'junk')"
	default:
		where = "f.id::text = " + args.add(folderID)
	}
	if o.Folder != SnoozedFolder && !everywhere {
		where += " AND (x.snooze_until IS NULL OR x.snooze_until <= now())"
	}
	switch o.View {
	case ViewFocused:
		where += " AND NOT x.other"
	case ViewOther:
		where += " AND x.other"
	}
	switch o.Filter {
	case "unread":
		where += " AND NOT x.seen"
	case "flagged":
		where += " AND x.flagged"
	case "tome":
		where += " AND x.to_addr ILIKE '%' || m.email || '%'"
	case "attach":
		where += " AND EXISTS (SELECT 1 FROM files fl WHERE fl.mail_id = x.id AND fl.content_id = '')"
	case "mention":
		where += ` AND x.body ILIKE '%@' || coalesce(nullif((SELECT split_part(trim(u.name), ' ', 1) FROM users u WHERE u.id = m.user_id), ''), chr(1)) || '%'`
	case "high":
		where += " AND x.importance = 1"
	}
	where = ` WHERE m.user_id::text = $1 AND ` + where + ` AND ` + ParseQuery(o.Q).where(&args)
	sort, ok := sortExpr[o.Sort]
	if !ok {
		o.Sort, sort = "date", sortExpr["date"]
	}
	dir := "DESC"
	if SortAsc(o.Sort) != o.Turn {
		dir = "ASC"
	}
	pinned, outerPinned := "x.pinned DESC, ", "pinned_any DESC, "
	if o.Sort != "date" { // pinned letters are on top of the newest only
		pinned, outerPinned = "", ""
	}
	limit := args.add(MaxList)
	cols := fmt.Sprintf(messageCols, "left(x.body, "+strconv.Itoa(previewLen)+") AS body")
	sql := `SELECT ` + cols + `, 1, (NOT x.seen)::int, ARRAY[x.id::text]` + fmt.Sprintf(messageExtra, "''") + messageFrom + where +
		` ORDER BY ` + pinned + sort[0] + ` ` + dir + ` NULLS LAST, coalesce(x.woke_at, x.sent_at) DESC LIMIT ` + limit
	if o.Conv { // the newest letter of each conversation stands for it; it is read, flagged, pinned when any of its letters is
		sql = `SELECT id, mailbox_id, email, folder_id, name, role, from_addr, from_name, to_addr, cc_addr, subject, body, message_id, thread,
			sent_at, unread = 0, flagged_any, flag_done, pinned_any, flag_due, snooze_until, categories, lead_id, attached_all, n, unread, ids,
				body_html, bcc_addr, importance, receipt_to, receipt_done, is_draft, send_at, draft_error, other_, answered, answered_at, flag_remind, unsub, unsubscribed_at, voting, vote, smime_sign, smime_signer, smime_enc FROM (
			SELECT ` + strings.NewReplacer("f.id::text,", "f.id::text AS folder_id,", "x.id::text,", "x.id::text AS id,",
			"x.mailbox_id::text,", "x.mailbox_id::text AS mailbox_id,", "x.flag_due::timestamptz", "x.flag_due::timestamptz AS flag_due",
			"x.categories::text[]", "x.categories::text[] AS categories", "coalesce(x.lead_id::text, '')", "coalesce(x.lead_id::text, '') AS lead_id", "= '')::int", "= '')::int AS attached").Replace(cols) + `,
				sum((SELECT count(*) FROM files fl WHERE fl.mail_id = x.id AND fl.content_id = '')) OVER w AS attached_all, count(*) OVER w AS n, count(*) FILTER (WHERE NOT x.seen) OVER w AS unread, array_agg(x.id::text) OVER w AS ids,
				bool_or(x.flagged) OVER w AS flagged_any, bool_or(x.pinned) OVER w AS pinned_any, coalesce(x.woke_at, x.sent_at) AS at,
				max(coalesce(x.woke_at, x.sent_at)) OVER w AS last_at, sum(` + sizeExpr + `) OVER w AS size_all, '' AS body_html, x.bcc_addr,
				x.importance::int AS importance, x.receipt_to, x.receipt_done, d.id IS NOT NULL AS is_draft, d.send_at, coalesce(d.error, '') AS draft_error,
				x.other AS other_, x.answered, x.answered_at, x.flag_remind, x.unsubscribe <> '' AS unsub, x.unsubscribed_at, x.voting, x.vote, x.smime_sign, x.smime_signer, x.smime_enc, row_number() OVER (PARTITION BY x.mailbox_id, coalesce(nullif(x.thread, ''), x.id::text) ORDER BY coalesce(x.woke_at, x.sent_at) DESC) AS rn` +
			messageFrom + where + `
			WINDOW w AS (PARTITION BY x.mailbox_id, coalesce(nullif(x.thread, ''), x.id::text))) c
			WHERE rn = 1 ORDER BY ` + outerPinned + sort[1] + ` ` + dir + ` NULLS LAST, last_at DESC LIMIT ` + limit
	}
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Message])
}

func (s *Store) message(ctx context.Context, userID, id string) (Message, error) {
	rows, err := s.db.Query(ctx, messageSQL+` WHERE x.id::text = $1 AND m.user_id::text = $2`, id, userID)
	if err != nil {
		return Message{}, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Message])
	if err != nil {
		return Message{}, err
	}
	if len(list) == 0 {
		return Message{}, ErrNotFound
	}
	return list[0], nil
}

// Message opens a letter of the user and marks it read, in go24 and on the server (in the background: the page
// does not wait for the mail server).
func (s *Store) Message(ctx context.Context, userID, id string) (Message, error) {
	m, err := s.message(ctx, userID, id)
	if err != nil {
		return m, err
	}
	if !m.Seen {
		s.readNow(ctx, userID, []string{m.ID})
		m.Seen = true
	}
	return m, nil
}

// Conversation opens a letter with its whole conversation, as Outlook's reading pane does: the letters of its
// thread in the mailbox, the oldest first — the deleted and the junk ones only when the letter is one of them.
// All of them are read now.
func (s *Store) Conversation(ctx context.Context, userID, id string) ([]Message, error) {
	return s.Thread(ctx, userID, id, true)
}

// Thread is Conversation; its letters are read now only when read is set (else the script marks them read later,
// as «Обработка сообщений» says).
func (s *Store) Thread(ctx context.Context, userID, id string, read bool) ([]Message, error) {
	m, err := s.message(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	list := []Message{m}
	if m.Thread != "" {
		rows, err := s.db.Query(ctx, messageSQL+` WHERE m.user_id::text = $1 AND x.mailbox_id::text = $2 AND x.thread = $3
			AND (f.role NOT IN ('trash', 'junk') OR f.role = $4 OR x.id::text = $5) ORDER BY x.sent_at, x.id`, userID, m.MailboxID, m.Thread, m.FolderRole, m.ID)
		if err != nil {
			return nil, err
		}
		if list, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Message]); err != nil {
			return nil, err
		}
	}
	var unread []string
	for i := range list {
		if read && !list[i].Seen {
			unread = append(unread, list[i].ID)
			list[i].Seen = true
		}
	}
	if len(unread) > 0 {
		s.readNow(ctx, userID, unread)
	}
	return list, nil
}

// readNow marks letters read in go24 at once and on the server in the background.
func (s *Store) readNow(ctx context.Context, userID string, ids []string) {
	if _, err := s.db.Exec(ctx, `UPDATE mail_messages SET seen = true WHERE id::text = ANY($1)`, ids); err != nil {
		if s.Log != nil {
			s.Log("mail: mark read", strings.Join(ids, ","), err)
		}
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		list, err := s.letters(ctx, userID, ids)
		if err == nil {
			err = s.store(ctx, list, imap.SeenFlag, true)
		}
		if err != nil && s.Log != nil {
			s.Log("mail: mark read on the server", strings.Join(ids, ","), err)
		}
	}()
}

// SetLead remembers the CRM lead made of a letter.
func (s *Store) SetLead(ctx context.Context, messageID, leadID string) error {
	_, err := s.db.Exec(ctx, `UPDATE mail_messages SET lead_id = $2 WHERE id::text = $1`, messageID, leadID)
	return err
}

func pgxCollectStrings(rows pgx.Rows) ([]string, error) {
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// File is an attachment kept with a letter.
type File struct {
	ID, Name, ContentType string
	Size                  int64
}

// Image: a picture the browser shows itself.
func (f File) Image() bool {
	return strings.HasPrefix(f.ContentType, "image/") && f.ContentType != "image/svg+xml"
}

// Files of a letter of the user.
func (s *Store) Files(ctx context.Context, userID, letterID string) ([]File, error) {
	rows, err := s.db.Query(ctx, `SELECT f.id, f.name, f.content_type, f.size FROM files f JOIN mail_messages x ON x.id = f.mail_id
		JOIN mailboxes m ON m.id = x.mailbox_id WHERE x.id::text = $1 AND m.user_id::text = $2 AND f.content_id = '' ORDER BY f.name`, letterID, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[File])
}

// FilesOf are the files of the user's letters by letter (the list's «Предварительный просмотр вложений»).
func (s *Store) FilesOf(ctx context.Context, userID string, letterIDs []string) (map[string][]File, error) {
	out := map[string][]File{}
	if len(letterIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT x.id::text, f.id, f.name, f.content_type, f.size FROM files f JOIN mail_messages x ON x.id = f.mail_id
		JOIN mailboxes m ON m.id = x.mailbox_id WHERE x.id::text = ANY($1) AND m.user_id::text = $2 AND f.content_id = '' ORDER BY x.sent_at, f.name`,
		letterIDs, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var f File
		if err := rows.Scan(&id, &f.ID, &f.Name, &f.ContentType, &f.Size); err != nil {
			return nil, err
		}
		out[id] = append(out[id], f)
	}
	return out, rows.Err()
}

// Pictures are the pictures of a letter's text (Content-ID → file id).
func (s *Store) Pictures(ctx context.Context, userID, letterID string) (map[string]string, error) {
	rows, err := s.db.Query(ctx, `SELECT f.content_id, f.id::text FROM files f JOIN mail_messages x ON x.id = f.mail_id
		JOIN mailboxes m ON m.id = x.mailbox_id WHERE x.id::text = $1 AND m.user_id::text = $2 AND f.content_id <> ''`, letterID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var cid, id string
		if err := rows.Scan(&cid, &id); err != nil {
			return nil, err
		}
		out[cid] = id
	}
	return out, rows.Err()
}

// Owns: is the letter in one of the user's mailboxes (its files are theirs to download)?
func (s *Store) Owns(ctx context.Context, userID, letterID string) bool {
	var ok bool
	_ = s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mail_messages x JOIN mailboxes m ON m.id = x.mailbox_id
		WHERE x.id::text = $1 AND m.user_id::text = $2)`, letterID, userID).Scan(&ok)
	return ok
}

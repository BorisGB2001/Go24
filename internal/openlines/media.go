package openlines

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// MaxFile is the largest file of a dialogue, the visitor's or the operator's.
const MaxFile = 25 << 20

// TypingFor is how long «печатает…» stays after the last keystroke.
const TypingFor = 6 * time.Second

// File is a file of a message, already in the built-in storage under Key.
type File struct {
	Key, Name, Type string
	Size            int64
}

func (f *File) name() string {
	if f == nil {
		return ""
	}
	return f.Name
}

// checkMessage: a message has a text (5000 characters at most), a file or both.
func checkMessage(body string, f *File) error {
	if (body == "" && f == nil) || len([]rune(body)) > 5000 {
		return i18n.E("err.ol_message")
	}
	if f != nil && (f.Key == "" || f.Name == "" || f.Size > MaxFile) {
		return i18n.E("err.ol_file")
	}
	return nil
}

// insertMessage adds the visitor's (authorID "") or an operator's message.
func insertMessage(ctx context.Context, q dbtx, sessionID, authorID, body string, f *File) error {
	return insertKind(ctx, q, sessionID, authorID, "", body, f)
}

// insertKind adds a message of the kind ("" — the visitor's or an operator's, hidden — an operator's hidden one).
func insertKind(ctx context.Context, q dbtx, sessionID, authorID, kind, body string, f *File) error {
	var file File
	secret := ""
	if f != nil {
		file = *f
		if len([]rune(file.Name)) > 200 {
			file.Name = string([]rune(file.Name)[:200])
		}
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		secret = hex.EncodeToString(b)
	}
	var author *string
	if authorID != "" {
		author = &authorID
	}
	_, err := q.Exec(ctx, `INSERT INTO ol_messages (session_id, author_id, body, file_key, file_name, file_type, file_size, file_secret, kind)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9)`, sessionID, author, body, file.Key, file.Name, file.Type, file.Size, secret, kind)
	return err
}

// StoredFile is a message's file and where it is: the dialogue and its line.
type StoredFile struct {
	File
	SessionID, WidgetID string
	secret              string
}

// FileOf is the file of a message; the visitor's side checks its secret (Secret).
func (s *Store) FileOf(ctx context.Context, msgID int64) (StoredFile, error) {
	var f StoredFile
	err := s.db.QueryRow(ctx, `SELECT m.file_key, m.file_name, m.file_type, m.file_size, m.session_id::text, s.widget_id::text, m.file_secret
		FROM ol_messages m JOIN ol_sessions s ON s.id = m.session_id WHERE m.id = $1 AND m.file_key <> ''`, msgID).
		Scan(&f.Key, &f.Name, &f.Type, &f.Size, &f.SessionID, &f.WidgetID, &f.secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, ErrNotFound
	}
	return f, err
}

// Secret: is it the file's secret (the link the widget got with the message)?
func (f StoredFile) Secret(v string) bool {
	return f.secret != "" && subtle.ConstantTimeCompare([]byte(f.secret), []byte(v)) == 1
}

// VisitorTyping: the visitor is typing — the operators' page of the dialogue shows it.
func (s *Store) VisitorTyping(ctx context.Context, widgetID, token string) error {
	id, err := s.session(ctx, widgetID, token)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `WITH u AS (UPDATE ol_sessions SET vis_typing_at = now() WHERE id = $1 RETURNING id)
		SELECT pg_notify('go24_openlines', 'ol:t:' || id::text) FROM u`, id)
	return err
}

// Typing: an operator is typing in the dialogue — the visitor's widget shows it.
func (s *Store) Typing(ctx context.Context, userID, id string) error {
	_, err := s.db.Exec(ctx, `UPDATE ol_sessions SET op_typing_at = now() WHERE id::text = $1 AND status = 'open'`, id)
	return err
}

// VisitorSeen: the visitor has seen the dialogue (the widget is open) — the operators see «прочитано».
func (s *Store) VisitorSeen(ctx context.Context, widgetID, token string) error {
	id, err := s.session(ctx, widgetID, token)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `WITH u AS (UPDATE ol_sessions SET vis_read = m.last FROM (SELECT max(id) AS last FROM ol_messages WHERE session_id = $1 AND kind NOT IN ('note', 'hidden')) m
		WHERE id = $1 AND m.last > vis_read RETURNING ol_sessions.id)
		SELECT pg_notify('go24_openlines', 'ol:' || id::text) FROM u`, id)
	return err
}

// Seen is what the widget shows besides the messages: the dialogue's operator (name, whether there is a photo), whether
// they are typing, and the last message the operators have read.
type Seen struct {
	OperatorID, Operator string
	Photo, Typing        bool
	Read                 int64
}

func (s *Store) VisitorSeenState(ctx context.Context, widgetID, token string) (Seen, error) {
	id, err := s.session(ctx, widgetID, token)
	if err != nil {
		return Seen{}, err
	}
	var x Seen
	err = s.db.QueryRow(ctx, `SELECT coalesce(s.operator_id::text, ''), coalesce(u.name, ''), coalesce(u.photo_key, '') <> '',
		s.op_typing_at IS NOT NULL AND s.op_typing_at > now() - make_interval(secs => $2), s.op_read
		FROM ol_sessions s LEFT JOIN users u ON u.id = s.operator_id WHERE s.id = $1`, id, TypingFor.Seconds()).
		Scan(&x.OperatorID, &x.Operator, &x.Photo, &x.Typing, &x.Read)
	return x, err
}

// OperatorPhoto is the storage key and type of the photo of an operator who has answered in the line (the widget shows
// it); "" — none.
func (s *Store) OperatorPhoto(ctx context.Context, widgetID, userID string) (key, typ string, err error) {
	err = s.db.QueryRow(ctx, `SELECT u.photo_key, u.photo_type FROM users u WHERE u.id::text = $2 AND EXISTS (SELECT 1 FROM ol_messages m
		JOIN ol_sessions s ON s.id = m.session_id WHERE s.widget_id::text = $1 AND m.author_id = u.id AND m.kind = '')`, widgetID, userID).Scan(&key, &typ)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	return key, typ, err
}

// FileLink is the widget's link to a message's file.
func FileLink(base, widgetID string, m Message) string {
	return base + "/w/" + widgetID + "/file/" + strconv.FormatInt(m.ID, 10) + "/" + m.FileSecret
}

// CleanName is a file's name without its folders.
func CleanName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	return name
}

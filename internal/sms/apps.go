package sms

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Providers of applications, as messageservice.* (GO24DEV-1027): an application adds its provider by a
// code with a handler on its host (messageservice.sender.add), go24 posts every message sent through it to the handler
// (type, code, message_id, message_to, message_body) and the application tells how it went
// (messageservice.message.status.update).

var (
	ErrAppSenderExists  = i18n.E("err.sms_app_exists")
	ErrAppSenderMissing = i18n.E("err.sms_app_missing")
	ErrAppSender        = i18n.E("err.sms_app_sender")
	ErrMessageMissing   = i18n.E("err.sms_message_missing")
	ErrMessageStatus    = i18n.E("err.sms_message_status")
)

var appCodeRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)

// AppStatuses are the statuses an application tells (the names) and what go24 keeps of them.
var AppStatuses = map[string]string{"queued": "sent", "sent": "sent", "delivered": "delivered", "undelivered": "undelivered", "failed": "error"}

// SaveAppSender adds the application's provider: Code, Name, Login (the handler, the caller checked it is on the
// application's host), Description. The first provider of go24 is the default one.
func (s *Store) SaveAppSender(ctx context.Context, appID string, x Sender) (string, error) {
	x.Code, x.Name, x.Login, x.Description = strings.TrimSpace(x.Code), strings.TrimSpace(x.Name), strings.TrimSpace(x.Login), strings.TrimSpace(x.Description)
	if !appCodeRe.MatchString(x.Code) || x.Name == "" || len([]rune(x.Name)) > 200 || len([]rune(x.Description)) > 1000 || len(x.Login) > 500 ||
		!strings.HasPrefix(x.Login, "http://") && !strings.HasPrefix(x.Login, "https://") {
		return "", ErrAppSender
	}
	err := s.db.QueryRow(ctx, `INSERT INTO sms_senders (kind, app_id, code, name, login, description, active, is_default)
		VALUES ('app', $1, $2, $3, $4, $5, true, NOT EXISTS (SELECT 1 FROM sms_senders WHERE is_default))
		ON CONFLICT (app_id, code) WHERE app_id IS NOT NULL DO NOTHING RETURNING id::text`,
		appID, x.Code, x.Name, x.Login, x.Description).Scan(&x.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrAppSenderExists
	}
	return x.ID, err
}

// AppSenders are the application's own providers.
func (s *Store) AppSenders(ctx context.Context, appID string) ([]Sender, error) {
	rows, err := s.db.Query(ctx, `SELECT `+senderCols+` FROM sms_senders WHERE app_id::text = $1 ORDER BY code`, appID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Sender])
}

// DeleteAppSender takes the application's provider of the code off.
func (s *Store) DeleteAppSender(ctx context.Context, appID, code string) error {
	var id string
	err := s.db.QueryRow(ctx, `SELECT id::text FROM sms_senders WHERE app_id::text = $1 AND code = $2`, appID, code).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAppSenderMissing
	}
	if err != nil {
		return err
	}
	return s.DeleteSender(ctx, id)
}

// SetAppStatus is what the application tells of a message it was given through its provider of the code: one of
// AppStatuses.
func (s *Store) SetAppStatus(ctx context.Context, appID, code, messageID, status string) error {
	st, ok := AppStatuses[strings.ToLower(strings.TrimSpace(status))]
	if !ok {
		return ErrMessageStatus
	}
	tag, err := s.db.Exec(ctx, `UPDATE sms_messages m SET status = $4, status_at = now() FROM sms_senders x
		WHERE x.id = m.sender_id AND x.app_id::text = $1 AND x.code = $2 AND m.id::text = $3`, appID, code, messageID, st)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrMessageMissing
	}
	return err
}

// callApp gives the message to the application's provider.
func (s *Store) callApp(ctx context.Context, x Sender, m Message, userID string) error {
	if s.App == nil {
		return errSender
	}
	data := map[string]any{"type": "SMS", "code": x.Code, "message_id": m.ID, "message_to": m.Phone, "message_body": m.Text}
	if x.From != "" {
		data["message_from"] = x.From
	}
	return s.App(ctx, x, userID, data)
}

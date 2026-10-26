package telephony

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Обратный звонок»: a client leaves their number in a form on a site. In the working hours of the
// form's line go24 asks Asterisk to ring the form's employees (all at once or by turns); the one who answers hears
// the ringing while Asterisk calls the client through the line (the client sees the line's number). The log has an
// outgoing call of the employee; nobody answered — a missed call of the client to the line. Out of the working hours
// the request waits: the employees are told to call back.

// Callback is a form of «Обратный звонок».
type Callback struct {
	ID, Token, Name string
	Trunk           string   // the external line
	Ring            []string // the employees rung
	RingMode        string   // all | queue
	Title, Text     string   // what the form says
	Button          string
}

// CallbackRequest is a client's request.
type CallbackRequest struct {
	ID, Number, Name, Page string
	Status                 string // calling | later
	Created                time.Time
}

// Statuses of a request.
const (
	CallbackCalling = "calling"
	CallbackLater   = "later"
)

// CallbackRecent is how long a number's request stands: another one of it in this time is not made.
const CallbackRecent = 5 * time.Minute

// ErrCallbackRecent: the number asked for a call just now.
var ErrCallbackRecent = errors.New("telephony: a request of the number just now")

// CallbackKey names the form's contexts in Asterisk.
func CallbackKey(id string) string { return "go24-cb-" + strings.ReplaceAll(id, "-", "")[:12] }

const callbackCols = `id::text, token, name, trunk::text, ring_users::text[], ring_mode, title, text, button`

func scanCallback(row pgx.Row) (Callback, error) {
	var c Callback
	err := row.Scan(&c.ID, &c.Token, &c.Name, &c.Trunk, &c.Ring, &c.RingMode, &c.Title, &c.Text, &c.Button)
	return c, err
}

// Callbacks are the forms by name.
func (s *Store) Callbacks(ctx context.Context) ([]Callback, error) {
	rows, err := s.db.Query(ctx, `SELECT `+callbackCols+` FROM telephony_callbacks ORDER BY lower(name), created_at, id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Callback, error) { return scanCallback(r) })
}

// CallbackByToken is the form of the public address; ErrNotFound — none.
func (s *Store) CallbackByToken(ctx context.Context, token string) (Callback, error) {
	c, err := scanCallback(s.db.QueryRow(ctx, `SELECT `+callbackCols+` FROM telephony_callbacks WHERE token = $1`, token))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// SaveCallback adds (ID "") or changes a form.
func (s *Store) SaveCallback(ctx context.Context, userID string, c Callback) (string, error) {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" || len([]rune(c.Name)) > 100 || strings.ContainsAny(c.Name, "\r\n") {
		return "", i18n.E("err.callback_name")
	}
	c.Title, c.Text, c.Button = strings.TrimSpace(c.Title), strings.TrimSpace(c.Text), strings.TrimSpace(c.Button)
	if len([]rune(c.Title)) > 200 || len([]rune(c.Text)) > 1000 || len([]rune(c.Button)) > 60 {
		return "", i18n.E("err.callback_text")
	}
	if c.RingMode != "queue" {
		c.RingMode = "all"
	}
	if c.Ring == nil {
		c.Ring = []string{}
	}
	if len(c.Ring) == 0 {
		return "", i18n.E("err.callback_ring")
	}
	var ok bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM telephony_trunks WHERE id::text = $1)`, c.Trunk).Scan(&ok); err != nil {
		return "", err
	}
	if !ok {
		return "", i18n.E("err.callback_trunk")
	}
	if c.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO telephony_callbacks (name, trunk, ring_users, ring_mode, title, text, button, created_by)
			VALUES ($1, $2::uuid, $3::uuid[], $4, $5, $6, $7, $8) RETURNING id::text`,
			c.Name, c.Trunk, c.Ring, c.RingMode, c.Title, c.Text, c.Button, userID).Scan(&c.ID)
		return c.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE telephony_callbacks SET name = $2, trunk = $3::uuid, ring_users = $4::uuid[], ring_mode = $5,
		title = $6, text = $7, button = $8 WHERE id::text = $1`, c.ID, c.Name, c.Trunk, c.Ring, c.RingMode, c.Title, c.Text, c.Button)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return c.ID, err
}

// DeleteCallback removes a form with its requests.
func (s *Store) DeleteCallback(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM telephony_callbacks WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// AddCallbackRequest keeps a client's request; ErrCallbackRecent — the number asked the form just now.
func (s *Store) AddCallbackRequest(ctx context.Context, callbackID string, r CallbackRequest) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `INSERT INTO telephony_callback_requests (callback, number, name, page, status)
		SELECT $1::uuid, $2, $3, $4, $5 WHERE NOT EXISTS (SELECT 1 FROM telephony_callback_requests
			WHERE callback = $1::uuid AND number = $2 AND created_at > now() - make_interval(secs => $6))
		RETURNING id::text`, callbackID, r.Number, r.Name, r.Page, r.Status, CallbackRecent.Seconds()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrCallbackRecent
	}
	return id, err
}

// SetCallbackStatus changes what became of a request.
func (s *Store) SetCallbackStatus(ctx context.Context, id, status string) error {
	_, err := s.db.Exec(ctx, `UPDATE telephony_callback_requests SET status = $2 WHERE id::text = $1`, id, status)
	return err
}

// CallbackRequests are the form's last requests, newest first.
func (s *Store) CallbackRequests(ctx context.Context, callbackID string, limit int) ([]CallbackRequest, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, number, name, page, status, created_at FROM telephony_callback_requests
		WHERE callback::text = $1 ORDER BY created_at DESC LIMIT $2`, callbackID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (CallbackRequest, error) {
		var q CallbackRequest
		err := r.Scan(&q.ID, &q.Number, &q.Name, &q.Page, &q.Status, &q.Created)
		return q, err
	})
}

// Working tells whether the line takes calls at t: it has no working hours, or t is in them.
func (t Trunk) Working(at time.Time) bool {
	if !t.WorkOn {
		return true
	}
	at = at.In(time.Local)
	day := int(at.Weekday())
	if day == 0 {
		day = 7
	}
	clock := at.Format("15:04")
	return slices.Contains(t.WorkDays, day) && clock >= t.WorkFrom && clock < t.WorkTo
}

// RenderCallbacks is the dialplan of the forms. go24 originates Local/<the client>@go24-cb-K/n: its half running
// go24-cb-K (…;2) rings the employees with the client's number shown; the employee who answers (go24-cb-who) becomes
// the caller of the other half (…;1, read by ${SHARED(GO24_FROM)}), which goes to go24-cb-K-out and calls the client
// through the line — the log gets the employee's
// outgoing call. Nobody answered — the client's missed call to the line. trunks are the lines by their ids, numbers —
// the employees' inner numbers by their ids.
func RenderCallbacks(cbs []Callback, trunks map[string]Trunk, numbers map[string]string, record bool) string {
	var d strings.Builder
	rec := ""
	if record {
		rec = " same => n,Set(GO24_RECORD=1)\n"
	}
	for _, c := range cbs {
		t, ok := trunks[c.Trunk]
		if !ok {
			continue
		}
		var ring []string
		for _, u := range c.Ring {
			if n := numbers[u]; n != "" {
				ring = append(ring, n)
			}
		}
		k := CallbackKey(c.ID)
		fmt.Fprintf(&d, "\n; %s\n[%s]\nexten => _X.,1,Set(GO24_CBNUM=${EXTEN})\n same => n,Goto(s,1)\nexten => _+X.,1,Set(GO24_CBNUM=${EXTEN})\n same => n,Goto(s,1)\n",
			callerName(c.Name), k)
		// «;» starts a comment in Asterisk's files
		fmt.Fprintf(&d, "exten => s,1,Set(GO24_CB=${CUT(CHANNEL,\\;,1)}\\;1)\n same => n,Set(CALLERID(num)=${GO24_CBNUM})\n same => n,Set(CALLERID(name)=%s)\n",
			callerName(c.Name))
		switch {
		case len(ring) == 0:
		case c.RingMode == "queue":
			for _, n := range ring {
				fmt.Fprintf(&d, " same => n,Dial(Local/%s@go24-ring,%d,U(go24-cb-who^${GO24_CB}))\n same => n,GotoIf($[\"${DIALSTATUS}\" = \"ANSWER\"]?done)\n", n, RingTurn)
			}
		default:
			legs := make([]string, len(ring))
			for i, n := range ring {
				legs[i] = "Local/" + n + "@go24-ring"
			}
			fmt.Fprintf(&d, " same => n,Dial(%s,45,U(go24-cb-who^${GO24_CB}))\n same => n,GotoIf($[\"${DIALSTATUS}\" = \"ANSWER\"]?done)\n", strings.Join(legs, "&"))
		}
		first := ""
		if len(ring) > 0 {
			first = ring[0]
		}
		// nobody answered: the client's missed call to the line
		fmt.Fprintf(&d, " same => n,Set(GO24_FROM=${GO24_CBNUM})\n same => n,Set(GO24_TO=%[1]s)\n same => n,Set(GO24_LINE=%[1]s)\n same => n,Set(GO24_BY=%[2]s)\n"+
			" same => n,Set(CHANNEL(hangup_handler_push)=go24-cdr,s,1)\n same => n(done),Hangup()\n", t.Number, first)
		// the employee answered: the client is called through the line
		fmt.Fprintf(&d, "\n[%[1]s-out]\nexten => _X.,1,Set(GO24_TO=${EXTEN})\n same => n,Goto(s,1)\nexten => _+X.,1,Set(GO24_TO=${EXTEN})\n same => n,Goto(s,1)\n"+
			"exten => s,1,Set(GO24_LINE=%[2]s)\n same => n,Set(GO24_FROM=${SHARED(GO24_FROM)})\n%[4]s same => n,Set(CHANNEL(hangup_handler_push)=go24-cdr,s,1)\n same => n,Set(CHANNEL(hangup_handler_push)=go24-cb-end,s,1)\n"+
			" same => n,Set(CALLERID(all)=%[2]s <%[2]s>)\n same => n,Gosub(go24-record,s,1)\n same => n,Dial(PJSIP/${GO24_TO}@%[3]s,60)\n same => n,Hangup()\n",
			k, t.Number, t.Key(), rec)
	}
	return d.String()
}

// Originate asks Asterisk to call Local/<number>@context (not optimized away: its hangup handlers run at the end of
// the call); its other half goes to <number>@outContext when answered.
func (a *ARI) Originate(ctx context.Context, number, context, outContext string) error {
	v := url.Values{"endpoint": {"Local/" + number + "@" + context + "/n"}, "extension": {number}, "context": {outContext}, "priority": {"1"},
		"timeout": {"200"}}
	return a.do(ctx, http.MethodPost, "/channels?"+v.Encode(), nil)
}

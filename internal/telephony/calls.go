package telephony

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Company settings: the conversations are recorded; an unknown number calling makes a lead (on unless turned off).
const (
	settingRecord = "telephony_record"
	settingLead   = "telephony_lead"
	settingText   = "telephony_transcribe"
)

// LeadAuto tells whether a call from an unknown number makes a lead.
func (s *Store) LeadAuto(ctx context.Context) (bool, error) {
	var on bool
	err := s.db.QueryRow(ctx, `SELECT value = 'true'::jsonb FROM company_settings WHERE key = $1`, settingLead).Scan(&on)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	return on, err
}

// SetLeadAuto turns the leads of unknown numbers on or off.
func (s *Store) SetLeadAuto(ctx context.Context, userID string, on bool) error {
	_, err := s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, to_jsonb($2::boolean), $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		settingLead, on, userID)
	return err
}

// Transcribing tells whether the records are turned into text by themselves after the call.
func (s *Store) Transcribing(ctx context.Context) (bool, error) {
	var on bool
	err := s.db.QueryRow(ctx, `SELECT value = 'true'::jsonb FROM company_settings WHERE key = $1`, settingText).Scan(&on)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return on, err
}

// SetTranscribing turns the records' text after the call on or off.
func (s *Store) SetTranscribing(ctx context.Context, userID string, on bool) error {
	_, err := s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, to_jsonb($2::boolean), $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		settingText, on, userID)
	return err
}

// SetTranscript keeps the text of the call's record.
func (s *Store) SetTranscript(ctx context.Context, id, text string) error {
	_, err := s.db.Exec(ctx, `UPDATE telephony_calls SET transcript = $2 WHERE id::text = $1`, id, text)
	return err
}

// Recording tells whether the conversations are recorded.
func (s *Store) Recording(ctx context.Context) (bool, error) {
	var on bool
	err := s.db.QueryRow(ctx, `SELECT value = 'true'::jsonb FROM company_settings WHERE key = $1`, settingRecord).Scan(&on)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return on, err
}

// SetRecording turns recording of the conversations on or off.
func (s *Store) SetRecording(ctx context.Context, userID string, on bool) error {
	_, err := s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, to_jsonb($2::boolean), $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		settingRecord, on, userID)
	return err
}

// CDR is what Asterisk tells about a call when it ends (deploy/asterisk/extensions.conf, go24-cdr).
type CDR struct {
	UniqueID, From, To string
	By                 string // the employee on go24's end of a call from an external line (who answered, or rang first)
	Line               string // the external line's number ("" — a call between employees)
	Dial               string // DIALSTATUS: ANSWER, BUSY, NOANSWER, CANCEL, CHANUNAVAIL, CONGESTION; "" — nobody to ring
	Record             string // the file of the conversation in the volume of recordings
	Voicemail          bool   // nobody answered, the client was asked to leave a message (Record — the message, if left)
	XFrom, XTo         string // transferred: by whom (an employee's inner number) and to which number
	Rating             int    // the client rated the conversation 1–5 (0 — not)
	Start, Answer, End time.Time
}

// Statuses of a call in the log.
const (
	StatusAnswered  = "answered"
	StatusBusy      = "busy"
	StatusNoAnswer  = "noanswer"
	StatusCancel    = "cancel"
	StatusFailed    = "failed"
	StatusVoicemail = "voicemail" // the client left a message
)

// Status of the call by Asterisk's DIALSTATUS.
func (c CDR) Status() string {
	if c.Voicemail { // the voicemail answers the call: it is not answered by the employees
		if c.Record != "" {
			return StatusVoicemail
		}
		return StatusNoAnswer
	}
	if !c.Answer.IsZero() || c.Dial == "ANSWER" {
		return StatusAnswered
	}
	switch c.Dial {
	case "BUSY":
		return StatusBusy
	case "NOANSWER":
		return StatusNoAnswer
	case "CANCEL":
		return StatusCancel
	}
	return StatusFailed
}

// SaveCall puts the call into the log (once: Asterisk may tell twice) with the employees on both ends; the record
// of a call nobody answered is dropped (a file of silence), the voicemail's message is kept (its length is the call's). The id of the call; "" — it was there already.
func (s *Store) SaveCall(ctx context.Context, c CDR) (string, error) {
	if c.UniqueID == "" || c.Start.IsZero() {
		return "", errors.New("telephony: a call without its id or start")
	}
	st := c.Status()
	var answered *time.Time
	secs := 0
	if !c.Answer.IsZero() {
		if !c.Voicemail {
			answered = &c.Answer
		}
		if !c.End.IsZero() && c.End.After(c.Answer) {
			secs = int(c.End.Sub(c.Answer).Round(time.Second) / time.Second)
		}
	}
	if (st != StatusAnswered && st != StatusVoicemail) || strings.ContainsAny(c.Record, "/\\") {
		c.Record = ""
	}
	if c.XTo = Dialable(c.XTo); c.XTo == "" || !numberRe.MatchString(c.XFrom) {
		c.XFrom, c.XTo = "", ""
	}
	if st != StatusAnswered || c.Rating < 1 || c.Rating > 5 {
		c.Rating = 0
	}
	var id string
	err := s.db.QueryRow(ctx, `WITH who AS (SELECT DISTINCT ON (inner_phone) inner_phone, id FROM users
			WHERE active AND NOT guest AND inner_phone IN ($2, $3, $9) ORDER BY inner_phone, created_at, id)
		INSERT INTO telephony_calls (uniqueid, from_number, to_number, from_user, to_user, started_at, answered_at, seconds, status, record, line,
			transfer_from, transfer_to, rating)
		VALUES ($1, $2, $3, (SELECT id FROM who WHERE inner_phone = $2),
			coalesce((SELECT id FROM who WHERE inner_phone = $3), CASE WHEN NOT EXISTS (SELECT 1 FROM who WHERE inner_phone = $2)
				THEN (SELECT id FROM who WHERE inner_phone = $9) END), $4, $5, $6, $7, $8, $10, $11, $12, $13)
		ON CONFLICT (uniqueid) DO NOTHING RETURNING id::text`,
		c.UniqueID, c.From, c.To, c.Start, answered, secs, st, c.Record, c.By, c.Line, c.XFrom, c.XTo, c.Rating).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// Call is a call of the log.
type Call struct {
	ID, From, To       string // numbers
	FromUser, ToUser   Who    // the employees on the ends (UserID "" — an outside number)
	Started            time.Time
	Answered           *time.Time
	Seconds            int
	Status, Record     string
	Line               string // the external line's number ("" — between employees)
	Transcript         string // the text of the record ("" — none yet)
	Rating             int    // the client's rating of the conversation, 1–5 (0 — none)
	XFrom, XTo         string // transferred by (an inner number) and to ("" — not transferred)
	XFromName, XToName string // the employees of those numbers ("" — none)
	// the client's CRM card the call went to (CRMEntity "" — none)
	CRMEntity, CRMID, CRMName string
}

// Outside is the outside end of the call: the number, whether it called in (or was called), and the employee on the
// other end; "" — a call between employees (or nobody of go24's on it).
func (c Call) Outside() (number, dir, employee string) {
	switch {
	case c.FromUser.UserID == "" && c.ToUser.UserID != "":
		return c.From, "in", c.ToUser.UserID
	case c.ToUser.UserID == "" && c.FromUser.UserID != "":
		return c.To, "out", c.FromUser.UserID
	}
	return "", "", ""
}

// CRMPath is the page of the client's card.
func (c Call) CRMPath() string {
	p := map[string]string{"deal": "deals", "lead": "leads", "contact": "contacts", "company": "companies"}[c.CRMEntity]
	if p == "" || c.CRMID == "" {
		return ""
	}
	return "/crm/" + p + "/" + c.CRMID
}

// SetClient remembers whose CRM card the call went to.
func (s *Store) SetClient(ctx context.Context, id, entity, crmID string) error {
	_, err := s.db.Exec(ctx, `UPDATE telephony_calls SET crm_entity = $2, crm_id = $3 WHERE id::text = $1`, id, entity, crmID)
	return err
}

// callCRM is the SQL of the client's card of a call c: its kind, id and name.
const callCRM = `c.crm_entity, coalesce(c.crm_id::text, ''), coalesce(cx.name, cc.title, cl.title, cd.title, '')`
const callCRMJoin = ` LEFT JOIN crm_contacts cx ON c.crm_entity = 'contact' AND cx.id = c.crm_id
	LEFT JOIN crm_companies cc ON c.crm_entity = 'company' AND cc.id = c.crm_id
	LEFT JOIN crm_leads cl ON c.crm_entity = 'lead' AND cl.id = c.crm_id
	LEFT JOIN crm_deals cd ON c.crm_entity = 'deal' AND cd.id = c.crm_id`

// Missed: nobody answered.
func (c Call) Missed() bool { return c.Status != StatusAnswered }

// Duration as m:ss (h:mm:ss for a long one).
func (c Call) Duration() string {
	if c.Seconds >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", c.Seconds/3600, c.Seconds/60%60, c.Seconds%60)
	}
	return fmt.Sprintf("%d:%02d", c.Seconds/60, c.Seconds%60)
}

// CallFilter: which calls of the log. UserID: the calls of one employee ("" — everyone's); Kind: "in", "out",
// "missed" (for UserID — theirs: incoming not answered); Number: a part of a number on either end; Users (not nil):
// only the calls with one of these employees on an end (the rights' «dept»).
type CallFilter struct {
	UserID, Kind, Number string
	Users                []string
	From, To             time.Time // started in [From, To); zero — no bound
	Before               time.Time // the next page: older than this
	Limit                int
}

// Calls of the log, newest first.
func (s *Store) Calls(ctx context.Context, f CallFilter) ([]Call, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	var from, to, before *time.Time
	if !f.From.IsZero() {
		from = &f.From
	}
	if !f.To.IsZero() {
		to = &f.To
	}
	if !f.Before.IsZero() {
		before = &f.Before
	}
	rows, err := s.db.Query(ctx, `SELECT c.id::text, c.from_number, c.to_number,
			coalesce(c.from_user::text, ''), coalesce(CASE WHEN fu.name <> '' THEN fu.name ELSE fu.email END, ''), coalesce(fu.photo_key, ''),
			coalesce(c.to_user::text, ''), coalesce(CASE WHEN tu.name <> '' THEN tu.name ELSE tu.email END, ''), coalesce(tu.photo_key, ''),
			c.started_at, c.answered_at, c.seconds, c.status, c.record, c.line, c.transcript, c.rating, c.transfer_from, c.transfer_to,
			`+callWho("c.transfer_from")+`, `+callWho("c.transfer_to")+`, `+callCRM+`
		FROM telephony_calls c LEFT JOIN users fu ON fu.id = c.from_user LEFT JOIN users tu ON tu.id = c.to_user`+callCRMJoin+`
		WHERE ($1 = '' OR c.from_user::text = $1 OR c.to_user::text = $1)
		  AND CASE $2
		      WHEN 'out' THEN CASE WHEN $1 = '' THEN c.from_user IS NOT NULL ELSE c.from_user::text = $1 END
		      WHEN 'in' THEN CASE WHEN $1 = '' THEN c.to_user IS NOT NULL ELSE c.to_user::text = $1 END
		      WHEN 'missed' THEN c.status <> 'answered' AND ($1 = '' OR c.to_user::text = $1)
		      ELSE true END
		  AND ($3 = '' OR c.from_number LIKE '%' || $3 || '%' OR c.to_number LIKE '%' || $3 || '%')
		  AND ($4::timestamptz IS NULL OR c.started_at >= $4) AND ($5::timestamptz IS NULL OR c.started_at < $5)
		  AND ($6::timestamptz IS NULL OR c.started_at < $6)
		  AND ($8::text[] IS NULL OR c.from_user::text = ANY($8) OR c.to_user::text = ANY($8))
		ORDER BY c.started_at DESC, c.id LIMIT $7`, f.UserID, f.Kind, Dialable(f.Number), from, to, before, f.Limit, f.Users)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Call, error) {
		var c Call
		err := r.Scan(&c.ID, &c.From, &c.To, &c.FromUser.UserID, &c.FromUser.Name, &c.FromUser.PhotoKey,
			&c.ToUser.UserID, &c.ToUser.Name, &c.ToUser.PhotoKey, &c.Started, &c.Answered, &c.Seconds, &c.Status, &c.Record, &c.Line,
			&c.Transcript, &c.Rating, &c.XFrom, &c.XTo, &c.XFromName, &c.XToName, &c.CRMEntity, &c.CRMID, &c.CRMName)
		return c, err
	})
}

// callWho is the name of the employee with the inner number of the column ("" — none).
func callWho(col string) string {
	return `coalesce((SELECT CASE WHEN u.name <> '' THEN u.name ELSE u.email END FROM users u WHERE ` + col + ` <> '' AND u.inner_phone = ` + col +
		` AND u.active AND NOT u.guest ORDER BY u.created_at, u.id LIMIT 1), '')`
}

// CallByID is one call of the log; ErrNotFound — none.
func (s *Store) CallByID(ctx context.Context, id string) (Call, error) {
	var c Call
	err := s.db.QueryRow(ctx, `SELECT c.id::text, c.from_number, c.to_number, coalesce(c.from_user::text, ''), coalesce(c.to_user::text, ''),
		c.started_at, c.answered_at, c.seconds, c.status, c.record, c.line, c.transcript, `+callCRM+` FROM telephony_calls c`+callCRMJoin+` WHERE c.id::text = $1`, id).
		Scan(&c.ID, &c.From, &c.To, &c.FromUser.UserID, &c.ToUser.UserID, &c.Started, &c.Answered, &c.Seconds, &c.Status, &c.Record, &c.Line,
			&c.Transcript, &c.CRMEntity, &c.CRMID, &c.CRMName)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

package telephony

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Calls of an outside PBX (a cloud PBX, a SIP provider's) told by REST telephony.externalcall.*: the PBX
// registers a call when it starts (go24 shows the employee the client's card), finishes it when it ends (it goes into
// the log as go24's own calls do), may attach its record. The external lines are telephony.externalLine.*.

// Types of an outside call (TYPE).
const (
	ExtOut      = 1
	ExtIn       = 2
	ExtInRedir  = 3
	ExtCallback = 4
	ExtInfo     = 5
)

// External is a call registered and not finished yet.
type External struct {
	CallID, UserID, Number, Line string
	Type                         int
	CRMEntity, CRMID             string // the client's card ("" — none)
	LeadCreated                  bool   // the card is a lead made for the call
	Started                      time.Time
	Record                       string  // a record attached before the call finished
	Notify                       []int64 // the notifications of the call shown
}

// Incoming: the client called.
func (e External) Incoming() bool {
	return e.Type == ExtIn || e.Type == ExtInRedir || e.Type == ExtInfo
}

// Finish is how the call ended.
type Finish struct {
	UserID       string // the employee who talked ("" — the one it was registered for)
	Seconds      int
	Code, Reason string // CALL_FAILED_CODE (200 — answered) and its words
	Cost         float64
	Currency     string
	Record       string // the file in Records ("" — the one attached, or none)
	Rating       int    // 1–5 (0 — none)
}

// Status of the call by CALL_FAILED_CODE.
func (f Finish) Status() string {
	switch f.Code {
	case "", "200":
		return StatusAnswered
	case "304":
		return StatusNoAnswer
	case "486":
		return StatusBusy
	case "603", "603-S":
		return StatusCancel
	}
	return StatusFailed
}

// RegisterExternal keeps a call starting; its CALL_ID is made here.
func (s *Store) RegisterExternal(ctx context.Context, e *External) error {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	if e.Started.IsZero() {
		e.Started = time.Now()
	}
	e.CallID = "externalCall." + hex.EncodeToString(b) + "." + strconv.FormatInt(time.Now().Unix(), 10)
	var crmID *string
	if e.CRMID != "" {
		crmID = &e.CRMID
	}
	_, err := s.db.Exec(ctx, `INSERT INTO telephony_external (call_id, user_id, number, line, type, crm_entity, crm_id, lead_created, started_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, e.CallID, e.UserID, e.Number, e.Line, e.Type, e.CRMEntity, crmID, e.LeadCreated, e.Started)
	return err
}

// ExternalCall is a call registered and not finished; ErrNotFound — none.
func (s *Store) ExternalCall(ctx context.Context, callID string) (External, error) {
	var e External
	err := s.db.QueryRow(ctx, `SELECT call_id, user_id::text, number, line, type, crm_entity, coalesce(crm_id::text, ''), lead_created,
			started_at, record, notify_ids FROM telephony_external WHERE call_id = $1`, callID).
		Scan(&e.CallID, &e.UserID, &e.Number, &e.Line, &e.Type, &e.CRMEntity, &e.CRMID, &e.LeadCreated, &e.Started, &e.Record, &e.Notify)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// SetExternalNotify remembers the notifications of the call shown.
func (s *Store) SetExternalNotify(ctx context.Context, callID string, ids []int64) error {
	_, err := s.db.Exec(ctx, `UPDATE telephony_external SET notify_ids = $2 WHERE call_id = $1`, callID, ids)
	return err
}

// AttachRecord gives the call its record: a finished one in the log, else the one registered; the record replaced
// ("" — none); ErrNotFound — no such call.
func (s *Store) AttachRecord(ctx context.Context, callID, record string) (string, error) {
	var old string
	err := s.db.QueryRow(ctx, `UPDATE telephony_calls c SET record = $2 FROM telephony_calls o
		WHERE c.id = o.id AND c.external AND c.uniqueid = $1 RETURNING o.record`, callID, record).Scan(&old)
	if errors.Is(err, pgx.ErrNoRows) {
		err = s.db.QueryRow(ctx, `UPDATE telephony_external x SET record = $2 FROM telephony_external o
			WHERE x.call_id = o.call_id AND x.call_id = $1 RETURNING o.record`, callID, record).Scan(&old)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return old, err
}

// FinishExternal puts the call into the log (the employee's inner number on their end) and forgets it as registered;
// the id of the call in the log.
func (s *Store) FinishExternal(ctx context.Context, e External, f Finish) (string, error) {
	if f.UserID == "" {
		f.UserID = e.UserID
	}
	if f.Record == "" {
		f.Record = e.Record
	}
	st := f.Status()
	var answered *time.Time
	if st == StatusAnswered {
		answered = &e.Started
	} else {
		f.Seconds = 0
	}
	if st != StatusAnswered || f.Rating < 1 || f.Rating > 5 {
		f.Rating = 0
	}
	if f.Code == "" {
		f.Code = "200"
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var inner string
	if err := tx.QueryRow(ctx, `SELECT coalesce(inner_phone, '') FROM users WHERE id = $1`, f.UserID).Scan(&inner); err != nil {
		return "", err
	}
	from, to, fromUser, toUser := inner, e.Number, &f.UserID, (*string)(nil)
	if e.Incoming() {
		from, to, fromUser, toUser = e.Number, inner, nil, &f.UserID
	}
	var crmID *string
	if e.CRMID != "" {
		crmID = &e.CRMID
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO telephony_calls (uniqueid, from_number, to_number, from_user, to_user, started_at, answered_at,
			seconds, status, record, line, rating, crm_entity, crm_id, cost, cost_currency, failed_code, failed_reason, external)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, true) RETURNING id::text`,
		e.CallID, from, to, fromUser, toUser, e.Started, answered, f.Seconds, st, f.Record, e.Line, f.Rating, e.CRMEntity, crmID,
		f.Cost, f.Currency, f.Code, f.Reason).Scan(&id); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM telephony_external WHERE call_id = $1`, e.CallID); err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

// CallExtra is what the log keeps of a call for voximplant.statistic.get besides Call.
type CallExtra struct {
	Num          int64
	UniqueID     string
	Cost         float64
	Currency     string
	Code, Reason string // told by the outside PBX ("" — go24's own call)
}

// CallExtras of the calls by their ids.
func (s *Store) CallExtras(ctx context.Context, ids []string) (map[string]CallExtra, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, num, uniqueid, cost::float8, cost_currency, failed_code, failed_reason
		FROM telephony_calls WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]CallExtra{}
	for rows.Next() {
		var id string
		var x CallExtra
		if err := rows.Scan(&id, &x.Num, &x.UniqueID, &x.Cost, &x.Currency, &x.Code, &x.Reason); err != nil {
			return nil, err
		}
		out[id] = x
	}
	return out, rows.Err()
}

// ExtLine is an external line of an outside PBX (telephony.externalLine.*).
type ExtLine struct {
	Number, Name  string
	CRMAutoCreate bool // a call of an unknown number makes a lead
	Created       time.Time
}

// ExtLines are the external lines by number.
func (s *Store) ExtLines(ctx context.Context) ([]ExtLine, error) {
	rows, err := s.db.Query(ctx, `SELECT number, name, crm_auto_create, created_at FROM telephony_ext_lines ORDER BY number`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ExtLine, error) {
		var l ExtLine
		err := r.Scan(&l.Number, &l.Name, &l.CRMAutoCreate, &l.Created)
		return l, err
	})
}

// ExtLineAutoCreate: a call of the line makes a lead (a line not added — yes).
func (s *Store) ExtLineAutoCreate(ctx context.Context, number string) (bool, error) {
	on := true
	err := s.db.QueryRow(ctx, `SELECT crm_auto_create FROM telephony_ext_lines WHERE number = $1`, number).Scan(&on)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	return on, err
}

// SaveExtLine adds the line or changes its name and «create in CRM» (add); update changes an existing one only
// (ErrNotFound — none). The name nil and auto nil keep what is there.
func (s *Store) SaveExtLine(ctx context.Context, number string, name *string, auto *bool, add bool) error {
	q := `UPDATE telephony_ext_lines SET name = coalesce($2, name), crm_auto_create = coalesce($3, crm_auto_create) WHERE number = $1`
	if add {
		q = `INSERT INTO telephony_ext_lines (number, name, crm_auto_create) VALUES ($1, coalesce($2, ''), coalesce($3, true))
			ON CONFLICT (number) DO UPDATE SET name = coalesce($2, telephony_ext_lines.name), crm_auto_create = coalesce($3, telephony_ext_lines.crm_auto_create)`
	}
	tag, err := s.db.Exec(ctx, q, number, name, auto)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// DeleteExtLine removes the line; ErrNotFound — none.
func (s *Store) DeleteExtLine(ctx context.Context, number string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM telephony_ext_lines WHERE number = $1`, number)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

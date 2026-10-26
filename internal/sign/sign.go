// Package sign is «Подпись»: a document is sent for signing to the company's employee and to the client,
// who sign in turn — the employee in go24, the client by a personal link, confirming with a code sent to their
// e-mail (a simple electronic signature). Signed by all, the document gets a signing sheet at its end.
package sign

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
)

var (
	ErrNotFound = i18n.E("err.not_found")
	ErrParties  = i18n.E("sign.err.parties")
	ErrTitle    = i18n.E("sign.err.title")
	ErrTurn     = i18n.E("sign.err.turn")
	ErrCode     = i18n.E("sign.err.code")
	ErrExpired  = i18n.E("sign.err.code_expired")
	ErrTries    = i18n.E("sign.err.code_tries")
	ErrAgain    = i18n.E("sign.err.code_again")
)

const (
	// CodeTTL is how long a code sent to the client is good for.
	CodeTTL = 10 * time.Minute
	// CodeTries is how many wrong codes end a code.
	CodeTries = 5
	// CodeAgain is how soon a new code may be sent.
	CodeAgain = time.Minute
)

// Filters of the list of «Подпись»: all, waiting for my signature, mine to sign («Мои документы»), being signed,
// signed, refused or cancelled; hr — the sendings of HR documents.
var Filters = []string{"", "me", "mine", "signing", "done", "closed", "templates", "hr"}

type Store struct{ db *pgxpool.Pool }

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// Doc is a document sent for signing.
type Doc struct {
	ID, Title, Entity, EntityID, CRMDocID string
	FileID, Hash, PDFFileID, ResultFile   string
	BatchID                               string // the sending of an HR document it is of
	ProjectID                             string // the collab it was sent from
	RemindDays                            int    // the signer is reminded every so many days (0 — never)
	Status                                string // signing | done | refused | cancelled
	AuthorID, Author                      string
	CreatedAt                             time.Time
	DoneAt                                *time.Time
	Parties                               []Party
	Observers                             []Observer // see the document and are told how it ended
}

// Observer is an employee who sees the document without signing it.
type Observer struct{ UserID, Name string }

// Party is a signer: an employee (UserID) or the client (Email, Token).
type Party struct {
	ID, UserID, Name, Email, Token string
	Side                           string // "" | employee (the employee an HR document is for)
	Role                           string // "" — signs | reviewer — approves in turn, with no signature (an employee)
	Ord                            int
	Status                         string // wait | ready | signed | refused
	CodeSent                       bool
	HasSignature                   bool // signed with a handwritten signature (PartySignature)
	Reminders                      int  // the reminders sent by themselves
	SignedAt, RemindedAt           *time.Time
	IP, UserAgent, Reason          string
}

// SideMax: no more signers than this of a side, the company's or the client's.
const SideMax = 10

// Employee: the signer signs in go24, not by a link.
func (p Party) Employee() bool { return p.UserID != "" }

// Reviewer: the employee approves the document in their turn instead of signing it.
func (p Party) Reviewer() bool { return p.Role == "reviewer" }

// Current is the signer whose turn it is (nil — none).
func (d Doc) Current() *Party {
	for i := range d.Parties {
		if d.Parties[i].Status == "ready" {
			return &d.Parties[i]
		}
	}
	return nil
}

// Party is the signer by id (nil — not of the document).
func (d Doc) Party(id string) *Party {
	for i := range d.Parties {
		if d.Parties[i].ID == id {
			return &d.Parties[i]
		}
	}
	return nil
}

// Sees: the user sent the document or signs it.
func (d Doc) Sees(userID string) bool {
	if d.AuthorID == userID {
		return true
	}
	for _, p := range d.Parties {
		if p.UserID == userID {
			return true
		}
	}
	for _, o := range d.Observers {
		if o.UserID == userID {
			return true
		}
	}
	return false
}

// Hash is the SHA-256 of a file, as the signing sheet shows it.
func Hash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func token() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Create keeps the document with its signers in their order; the first one's turn comes at once.
func (s *Store) Create(ctx context.Context, d Doc, parties []Party) (string, error) {
	d.Title = strings.TrimSpace(d.Title)
	if d.Title == "" || len([]rune(d.Title)) > 300 {
		return "", ErrTitle
	}
	if len(parties) == 0 {
		return "", ErrParties
	}
	for _, p := range parties {
		if strings.TrimSpace(p.Name) == "" || (p.UserID == "" && !strings.Contains(p.Email, "@")) || (p.Reviewer() && p.UserID == "") {
			return "", ErrParties
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO sign_docs (title, entity, entity_id, crm_doc_id, file_id, hash, created_by, batch_id, remind_days, project_id)
		VALUES ($1, nullif($2, ''), nullif($3, '')::uuid, nullif($4, '')::uuid, $5, $6, $7, nullif($8, '')::uuid, $9, nullif($10, '')::uuid) RETURNING id`,
		d.Title, d.Entity, d.EntityID, d.CRMDocID, d.FileID, d.Hash, d.AuthorID, d.BatchID, RemindOf(d.RemindDays), d.ProjectID).Scan(&id); err != nil {
		return "", err
	}
	for i, p := range parties {
		status, tok := "wait", ""
		if i == 0 {
			status = "ready"
		}
		if p.UserID == "" {
			tok = token()
		}
		if _, err := tx.Exec(ctx, `INSERT INTO sign_parties (doc_id, ord, user_id, name, email, token, status, side, role, ready_at)
			VALUES ($1, $2, nullif($3, '')::uuid, $4, $5, nullif($6, ''), $7, $8, $9, CASE WHEN $7 = 'ready' THEN now() END)`,
			id, i+1, p.UserID, strings.TrimSpace(p.Name), strings.TrimSpace(p.Email), tok, status, p.Side, p.Role); err != nil {
			return "", err
		}
	}
	for _, o := range d.Observers {
		if _, err := tx.Exec(ctx, `INSERT INTO sign_observers (doc_id, user_id) VALUES ($1, $2::uuid) ON CONFLICT DO NOTHING`,
			id, o.UserID); err != nil {
			return "", err
		}
	}
	if err := logTx(ctx, tx, id, "", d.AuthorID, EvCreated, "", ""); err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

const docCols = `d.id, d.title, coalesce(d.entity, ''), coalesce(d.entity_id::text, ''), coalesce(d.crm_doc_id::text, ''),
	d.file_id::text, d.hash, coalesce(d.pdf_file_id::text, ''), coalesce(d.result_file_id::text, ''), d.status,
	d.created_by::text, u.name, d.created_at, d.done_at, coalesce(d.batch_id::text, ''), d.remind_days, coalesce(d.project_id::text, '')
	FROM sign_docs d JOIN users u ON u.id = d.created_by`

func scanDoc(r pgx.CollectableRow) (Doc, error) {
	var d Doc
	return d, r.Scan(&d.ID, &d.Title, &d.Entity, &d.EntityID, &d.CRMDocID, &d.FileID, &d.Hash, &d.PDFFileID, &d.ResultFile,
		&d.Status, &d.AuthorID, &d.Author, &d.CreatedAt, &d.DoneAt, &d.BatchID, &d.RemindDays, &d.ProjectID)
}

// parties puts the signers and the observers into the documents.
func (s *Store) parties(ctx context.Context, docs []Doc) error {
	if len(docs) == 0 {
		return nil
	}
	ids := make([]string, len(docs))
	at := map[string]int{}
	for i, d := range docs {
		ids[i], at[d.ID] = d.ID, i
	}
	rows, err := s.db.Query(ctx, `SELECT doc_id::text, id, coalesce(user_id::text, ''), name, email, coalesce(token, ''), side, role, ord,
			status, code_until IS NOT NULL, signature IS NOT NULL, reminders, signed_at, reminded_at, ip, user_agent, reason
		FROM sign_parties WHERE doc_id::text = ANY($1) ORDER BY doc_id, ord`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var doc string
		var p Party
		if err := rows.Scan(&doc, &p.ID, &p.UserID, &p.Name, &p.Email, &p.Token, &p.Side, &p.Role, &p.Ord, &p.Status, &p.CodeSent, &p.HasSignature, &p.Reminders, &p.SignedAt,
			&p.RemindedAt, &p.IP, &p.UserAgent, &p.Reason); err != nil {
			return err
		}
		docs[at[doc]].Parties = append(docs[at[doc]].Parties, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = s.db.Query(ctx, `SELECT o.doc_id::text, o.user_id::text, u.name FROM sign_observers o JOIN users u ON u.id = o.user_id
		WHERE o.doc_id::text = ANY($1) ORDER BY o.doc_id, lower(u.name)`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var doc string
		var o Observer
		if err := rows.Scan(&doc, &o.UserID, &o.Name); err != nil {
			return err
		}
		docs[at[doc]].Observers = append(docs[at[doc]].Observers, o)
	}
	return rows.Err()
}

func (s *Store) Get(ctx context.Context, id string) (Doc, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+docCols+` WHERE d.id::text = $1`, id)
	d, err := pgx.CollectExactlyOneRow(rows, scanDoc)
	if errors.Is(err, pgx.ErrNoRows) {
		return Doc{}, ErrNotFound
	}
	if err != nil {
		return Doc{}, err
	}
	docs := []Doc{d}
	err = s.parties(ctx, docs)
	return docs[0], err
}

// ByToken is the document and the client of a personal link.
func (s *Store) ByToken(ctx context.Context, tok string) (Doc, *Party, error) {
	var id string
	if tok == "" {
		return Doc{}, nil, ErrNotFound
	}
	err := s.db.QueryRow(ctx, `SELECT doc_id::text FROM sign_parties WHERE token = $1`, tok).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Doc{}, nil, ErrNotFound
	}
	if err != nil {
		return Doc{}, nil, err
	}
	d, err := s.Get(ctx, id)
	if err != nil {
		return Doc{}, nil, err
	}
	for i := range d.Parties {
		if d.Parties[i].Token == tok {
			return d, &d.Parties[i], nil
		}
	}
	return Doc{}, nil, ErrNotFound
}

// List is the user's documents (sent, to sign or observed) by the filter, the newest first; admin — everyone's.
func (s *Store) List(ctx context.Context, userID, filter string, admin bool) ([]Doc, error) {
	where := `(d.created_by = $1 OR EXISTS (SELECT 1 FROM sign_parties p WHERE p.doc_id = d.id AND p.user_id = $1)
		OR EXISTS (SELECT 1 FROM sign_observers o WHERE o.doc_id = d.id AND o.user_id = $1) OR $2)`
	switch filter {
	case "me":
		where = `EXISTS (SELECT 1 FROM sign_parties p WHERE p.doc_id = d.id AND p.user_id = $1 AND p.status = 'ready') AND d.status = 'signing' AND $2::bool IS NOT NULL`
	case "mine":
		where = `EXISTS (SELECT 1 FROM sign_parties p WHERE p.doc_id = d.id AND p.user_id = $1) AND $2::bool IS NOT NULL`
	case "signing", "done":
		where += ` AND d.status = '` + filter + `'`
	case "closed":
		where += ` AND d.status IN ('refused', 'cancelled')`
	case "hr", "templates":
		return nil, nil // the sendings, Batches; the templates, Templates
	}
	rows, _ := s.db.Query(ctx, `SELECT `+docCols+` WHERE `+where+` ORDER BY d.created_at DESC LIMIT 500`, userID, admin)
	docs, err := pgx.CollectRows(rows, scanDoc)
	if err != nil {
		return nil, err
	}
	return docs, s.parties(ctx, docs)
}

// Waiting is how many documents wait for the user's signature.
func (s *Store) Waiting(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM sign_parties p JOIN sign_docs d ON d.id = p.doc_id
		WHERE p.user_id = $1 AND p.status = 'ready' AND d.status = 'signing'`, userID).Scan(&n)
	return n, err
}

// OfCard are the documents sent from a CRM card, by the card's document they were made of.
func (s *Store) OfCard(ctx context.Context, entity, id string) (map[string]Doc, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+docCols+` WHERE d.entity = $1 AND d.entity_id::text = $2 AND d.crm_doc_id IS NOT NULL
		ORDER BY d.created_at`, entity, id)
	docs, err := pgx.CollectRows(rows, scanDoc)
	if err != nil {
		return nil, err
	}
	out := map[string]Doc{}
	for _, d := range docs {
		out[d.CRMDocID] = d // the latest sending of a document
	}
	return out, nil
}

func codeHash(partyID, code string) string { return Hash([]byte(partyID + ":" + code)) }

// SetCode keeps the code sent to the client for CodeTTL; a new code ends the old one, not sooner than CodeAgain
// after it.
func (s *Store) SetCode(ctx context.Context, partyID, code string) error {
	var early bool
	err := s.db.QueryRow(ctx, `SELECT coalesce(code_until > now() + $2::interval, false) FROM sign_parties
		WHERE id::text = $1 AND status = 'ready'`, partyID, (CodeTTL - CodeAgain).String()).Scan(&early)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrTurn
	case err != nil:
		return err
	case early:
		return ErrAgain
	}
	_, err = s.db.Exec(ctx, `UPDATE sign_parties SET code_hash = $2, code_until = now() + $3::interval, code_tries = 0
		WHERE id::text = $1`, partyID, codeHash(partyID, code), CodeTTL.String())
	return err
}

// ClearCode ends the code (one that could not be sent: a new one may be sent at once).
func (s *Store) ClearCode(ctx context.Context, partyID string) error {
	_, err := s.db.Exec(ctx, `UPDATE sign_parties SET code_hash = '', code_until = NULL WHERE id::text = $1`, partyID)
	return err
}

// Sign signs the document for the signer whose turn it is: the client with the code they were sent (code — ""
// for an employee); then it is the next one's turn. done — the document is signed by all.
func (s *Store) Sign(ctx context.Context, docID, partyID, code, ip, ua string) (next *Party, done bool, err error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	var ord, tries int
	var employee, live bool
	var hash, role string
	err = tx.QueryRow(ctx, `SELECT p.ord, p.user_id IS NOT NULL, p.code_hash, coalesce(p.code_until > now(), false), p.code_tries, p.role
		FROM sign_parties p JOIN sign_docs d ON d.id = p.doc_id
		WHERE p.id::text = $1 AND d.id::text = $2 AND p.status = 'ready' AND d.status = 'signing' FOR UPDATE OF p, d`,
		partyID, docID).Scan(&ord, &employee, &hash, &live, &tries, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, ErrTurn
	}
	if err != nil {
		return nil, false, err
	}
	if !employee {
		switch {
		case hash == "" || tries >= CodeTries:
			return nil, false, ErrTries
		case !live:
			return nil, false, ErrExpired
		case codeHash(partyID, strings.TrimSpace(code)) != hash:
			if _, err := tx.Exec(ctx, `UPDATE sign_parties SET code_tries = code_tries + 1 WHERE id::text = $1`, partyID); err != nil {
				return nil, false, err
			}
			if err := tx.Commit(ctx); err != nil {
				return nil, false, err
			}
			if tries+1 >= CodeTries {
				return nil, false, ErrTries
			}
			return nil, false, ErrCode
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE sign_parties SET status = 'signed', signed_at = now(), ip = $2, user_agent = $3,
			code_hash = '', code_until = NULL WHERE id::text = $1`, partyID, ip, truncate(ua, 300)); err != nil {
		return nil, false, err
	}
	kind := EvSigned
	if role == "reviewer" {
		kind = EvApproved
	}
	if err := logTx(ctx, tx, docID, partyID, "", kind, ip, ""); err != nil {
		return nil, false, err
	}
	var n Party
	err = tx.QueryRow(ctx, `UPDATE sign_parties SET status = 'ready', ready_at = now() WHERE doc_id::text = $1 AND ord = $2
		RETURNING id, coalesce(user_id::text, ''), name, email, coalesce(token, ''), ord, status`, docID, ord+1).
		Scan(&n.ID, &n.UserID, &n.Name, &n.Email, &n.Token, &n.Ord, &n.Status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := tx.Exec(ctx, `UPDATE sign_docs SET status = 'done', done_at = now() WHERE id::text = $1`, docID); err != nil {
			return nil, false, err
		}
		if err := logTx(ctx, tx, docID, "", "", EvDone, "", ""); err != nil {
			return nil, false, err
		}
		return nil, true, tx.Commit(ctx)
	case err != nil:
		return nil, false, err
	}
	return &n, false, tx.Commit(ctx)
}

// Refuse: the signer whose turn it is refuses to sign, with the reason; the document is refused.
func (s *Store) Refuse(ctx context.Context, docID, partyID, reason, ip, ua string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var role string
	err = tx.QueryRow(ctx, `UPDATE sign_parties p SET status = 'refused', signed_at = now(), reason = $3, ip = $4, user_agent = $5
		FROM sign_docs d WHERE d.id = p.doc_id AND p.id::text = $1 AND d.id::text = $2 AND p.status = 'ready' AND d.status = 'signing'
		RETURNING p.role`, partyID, docID, truncate(strings.TrimSpace(reason), 1000), ip, truncate(ua, 300)).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTurn
	}
	if err != nil {
		return err
	}
	kind := EvRefused
	if role == "reviewer" {
		kind = EvRejected
	}
	if _, err := tx.Exec(ctx, `UPDATE sign_docs SET status = 'refused', done_at = now() WHERE id::text = $1`, docID); err != nil {
		return err
	}
	if err := logTx(ctx, tx, docID, partyID, "", kind, ip, strings.TrimSpace(reason)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Cancel: the sender (actorID) recalls the document while it is being signed.
func (s *Store) Cancel(ctx context.Context, docID, actorID string) error {
	tag, err := s.db.Exec(ctx, `WITH c AS (UPDATE sign_docs SET status = 'cancelled', done_at = now() WHERE id::text = $1 AND status = 'signing' RETURNING id)
		INSERT INTO sign_events (doc_id, actor_id, kind) SELECT id, nullif($2, '')::uuid, 'cancelled' FROM c`, docID, actorID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrTurn
	}
	return err
}

// SetPDF keeps the document's PDF (to read it), SetResult — the signed document.
func (s *Store) SetPDF(ctx context.Context, id, fileID string) error {
	_, err := s.db.Exec(ctx, `UPDATE sign_docs SET pdf_file_id = $2 WHERE id::text = $1`, id, fileID)
	return err
}

func (s *Store) SetResult(ctx context.Context, id, fileID string) error {
	_, err := s.db.Exec(ctx, `UPDATE sign_docs SET result_file_id = $2 WHERE id::text = $1`, id, fileID)
	return err
}

// ---------- «Подпись для сотрудников» (КЭДО): an HR document sent to many employees, each their own copy ----------

// Batch is a sending of an HR document with how its copies stand.
type Batch struct {
	ID, Title, TemplateID     string
	SignerID, Signer          string // the company's representative signing first ("" — none)
	AuthorID, Author          string
	CreatedAt                 time.Time
	Total, Signed, Refused    int
	Cancelled, Signing, Ready int // Ready — copies waiting for the representative
}

// NewBatch keeps a sending; its copies are made by Create with Doc.BatchID.
func (s *Store) NewBatch(ctx context.Context, b Batch) (string, error) {
	b.Title = strings.TrimSpace(b.Title)
	if b.Title == "" || len([]rune(b.Title)) > 300 {
		return "", ErrTitle
	}
	var id string
	err := s.db.QueryRow(ctx, `INSERT INTO sign_batches (title, template_id, signer_id, created_by)
		VALUES ($1, nullif($2, '')::uuid, nullif($3, '')::uuid, $4) RETURNING id`, b.Title, b.TemplateID, b.SignerID, b.AuthorID).Scan(&id)
	return id, err
}

// DropBatch removes a sending none of whose copies could be made.
func (s *Store) DropBatch(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sign_batches WHERE id::text = $1`, id)
	return err
}

const batchCols = `b.id, b.title, coalesce(b.template_id::text, ''), coalesce(b.signer_id::text, ''), coalesce(su.name, ''),
	b.created_by::text, u.name, b.created_at,
	(SELECT count(*) FROM sign_docs d WHERE d.batch_id = b.id),
	(SELECT count(*) FROM sign_docs d WHERE d.batch_id = b.id AND d.status = 'done'),
	(SELECT count(*) FROM sign_docs d WHERE d.batch_id = b.id AND d.status = 'refused'),
	(SELECT count(*) FROM sign_docs d WHERE d.batch_id = b.id AND d.status = 'cancelled'),
	(SELECT count(*) FROM sign_docs d WHERE d.batch_id = b.id AND d.status = 'signing'),
	(SELECT count(*) FROM sign_docs d JOIN sign_parties p ON p.doc_id = d.id AND p.status = 'ready' AND p.user_id = b.signer_id
		WHERE d.batch_id = b.id AND d.status = 'signing')
	FROM sign_batches b JOIN users u ON u.id = b.created_by LEFT JOIN users su ON su.id = b.signer_id`

func scanBatch(r pgx.CollectableRow) (Batch, error) {
	var b Batch
	return b, r.Scan(&b.ID, &b.Title, &b.TemplateID, &b.SignerID, &b.Signer, &b.AuthorID, &b.Author, &b.CreatedAt,
		&b.Total, &b.Signed, &b.Refused, &b.Cancelled, &b.Signing, &b.Ready)
}

// Batches are the sendings the user made or signs for the company, the newest first; admin — everyone's.
func (s *Store) Batches(ctx context.Context, userID string, admin bool) ([]Batch, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+batchCols+` WHERE b.created_by = $1 OR b.signer_id = $1 OR $2
		ORDER BY b.created_at DESC LIMIT 500`, userID, admin)
	return pgx.CollectRows(rows, scanBatch)
}

// Batch is the sending with its copies, by the employees' names.
func (s *Store) Batch(ctx context.Context, id string) (Batch, []Doc, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+batchCols+` WHERE b.id::text = $1`, id)
	b, err := pgx.CollectExactlyOneRow(rows, scanBatch)
	if errors.Is(err, pgx.ErrNoRows) {
		return Batch{}, nil, ErrNotFound
	}
	if err != nil {
		return Batch{}, nil, err
	}
	rows, _ = s.db.Query(ctx, `SELECT `+docCols+` WHERE d.batch_id::text = $1
		ORDER BY (SELECT lower(p.name) FROM sign_parties p WHERE p.doc_id = d.id AND p.side = 'employee')`, id)
	docs, err := pgx.CollectRows(rows, scanDoc)
	if err == nil {
		err = s.parties(ctx, docs)
	}
	return b, docs, err
}

// CancelBatch: actorID recalls the copies still being signed; how many.
func (s *Store) CancelBatch(ctx context.Context, id, actorID string) (int, error) {
	tag, err := s.db.Exec(ctx, `WITH c AS (UPDATE sign_docs SET status = 'cancelled', done_at = now() WHERE batch_id::text = $1 AND status = 'signing' RETURNING id)
		INSERT INTO sign_events (doc_id, actor_id, kind) SELECT id, nullif($2, '')::uuid, 'cancelled' FROM c`, id, actorID)
	return int(tag.RowsAffected()), err
}

// Employee is the employee an HR document is for (nil — not an HR document).
func (d Doc) Employee() *Party {
	for i := range d.Parties {
		if d.Parties[i].Side == "employee" {
			return &d.Parties[i]
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

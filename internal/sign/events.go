package sign

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Kinds of the events of a document's history, as «История».
const (
	EvCreated   = "created"   // the sender made the document
	EvSent      = "sent"      // the signer's turn: told (a notification, a letter with the link)
	EvReminded  = "reminded"  // reminded by itself
	EvViewed    = "viewed"    // the signer opened the document (once)
	EvCode      = "code"      // the client asked for a code
	EvSigned    = "signed"    // the signer signed
	EvRefused   = "refused"   // the signer refused, Note — the reason
	EvCancelled = "cancelled" // the sender recalled it
	EvDone      = "done"      // signed by all
)

// Event is a line of the document's history.
type Event struct {
	Kind           string
	PartyID, Party string // the signer it is about ("" — the document)
	ActorID, Actor string // the employee who did it ("" — the client by the link or go24 itself)
	OK             bool   // false — the letter was not sent
	IP, Note       string
	At             time.Time
}

// Log adds an event to the document's history; a view is kept once for a signer.
func (s *Store) Log(ctx context.Context, docID, partyID, actorID, kind string, ok bool, ip, note string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO sign_events (doc_id, party_id, actor_id, kind, ok, ip, note)
		VALUES ($1::uuid, nullif($2, '')::uuid, nullif($3, '')::uuid, $4, $5, $6, $7) ON CONFLICT DO NOTHING`,
		docID, partyID, actorID, kind, ok, ip, truncate(note, 1000))
	return err
}

// logTx is Log within a transaction.
func logTx(ctx context.Context, tx pgx.Tx, docID, partyID, actorID, kind, ip, note string) error {
	_, err := tx.Exec(ctx, `INSERT INTO sign_events (doc_id, party_id, actor_id, kind, ip, note)
		VALUES ($1::uuid, nullif($2, '')::uuid, nullif($3, '')::uuid, $4, $5, $6)`, docID, partyID, actorID, kind, ip, truncate(note, 1000))
	return err
}

// Events is the document's history, the oldest first.
func (s *Store) Events(ctx context.Context, docID string) ([]Event, error) {
	rows, _ := s.db.Query(ctx, `SELECT e.kind, coalesce(e.party_id::text, ''), coalesce(p.name, ''), coalesce(e.actor_id::text, ''),
			coalesce(u.name, ''), e.ok, e.ip, e.note, e.created_at
		FROM sign_events e LEFT JOIN sign_parties p ON p.id = e.party_id LEFT JOIN users u ON u.id = e.actor_id
		WHERE e.doc_id::text = $1 ORDER BY e.created_at, e.id`, docID)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Event, error) {
		var e Event
		return e, r.Scan(&e.Kind, &e.PartyID, &e.Party, &e.ActorID, &e.Actor, &e.OK, &e.IP, &e.Note, &e.At)
	})
}

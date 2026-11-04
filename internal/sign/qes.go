package sign

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"go24/internal/qes"
)

// SetQES keeps the signer's qualified signature of the document, already checked (qes.Verify), with what its
// certificate says; Sign then signs for them with no code. Only the signer whose turn it is and who signs with КЭП.
func (s *Store) SetQES(ctx context.Context, docID, partyID string, sig []byte, info qes.Signed) error {
	tag, err := s.db.Exec(ctx, `UPDATE sign_parties p SET qes_sig = $3, qes = $4 FROM sign_docs d
		WHERE d.id = p.doc_id AND p.id::text = $1 AND d.id::text = $2 AND p.status = 'ready' AND p.method = 'qes' AND d.status = 'signing'`,
		partyID, docID, sig, info)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrTurn
	}
	return err
}

// QESSig is the signer's detached signature (.sig) of the document.
func (s *Store) QESSig(ctx context.Context, docID, partyID string) ([]byte, error) {
	var sig []byte
	err := s.db.QueryRow(ctx, `SELECT qes_sig FROM sign_parties WHERE id::text = $1 AND doc_id::text = $2 AND status = 'signed' AND qes_sig IS NOT NULL`,
		partyID, docID).Scan(&sig)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sig, err
}

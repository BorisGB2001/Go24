package sign

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	_ "image/jpeg" // a signature may be uploaded as a JPEG
	"image/png"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// ErrSignature: the handwritten signature is not a picture, or too big.
var ErrSignature = i18n.E("sign.err.signature")

const (
	// SignatureMax is the most a handwritten signature may weigh, as sent.
	SignatureMax = 1 << 20
	// signatureSide is the most pixels a side of it may have.
	signatureSide = 2000
)

// Signature is the signer's handwritten signature — drawn or uploaded: a PNG or a JPEG, plain or as
// a data URL («data:image/png;base64,…»); it is kept as a PNG made anew. "" — no signature (nil, no error).
func Signature(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if strings.HasPrefix(s, "data:") {
		_, s, _ = strings.Cut(s, ",")
	}
	if base64.StdEncoding.DecodedLen(len(s)) > SignatureMax {
		return nil, ErrSignature
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, ErrSignature
	}
	return SignatureImage(data)
}

// SignatureImage is an uploaded picture as a handwritten signature.
func SignatureImage(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data) > SignatureMax {
		return nil, ErrSignature
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width == 0 || cfg.Height == 0 || cfg.Width > signatureSide || cfg.Height > signatureSide {
		return nil, ErrSignature
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrSignature
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// SetSignature keeps the handwritten signature the signer signed with.
func (s *Store) SetSignature(ctx context.Context, partyID string, img []byte) error {
	_, err := s.db.Exec(ctx, `UPDATE sign_parties SET signature = $2 WHERE id::text = $1`, partyID, img)
	return err
}

// PartySignature is the signer's handwritten signature (nil — none).
func (s *Store) PartySignature(ctx context.Context, partyID string) ([]byte, error) {
	var img []byte
	err := s.db.QueryRow(ctx, `SELECT signature FROM sign_parties WHERE id::text = $1`, partyID).Scan(&img)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return img, err
}

// MySignature is the signature the employee keeps to sign with (nil — none).
func (s *Store) MySignature(ctx context.Context, userID string) ([]byte, error) {
	var img []byte
	err := s.db.QueryRow(ctx, `SELECT image FROM sign_my_signatures WHERE user_id::text = $1`, userID).Scan(&img)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return img, err
}

// KeepMySignature keeps the employee's signature to sign with next time; nil forgets it.
func (s *Store) KeepMySignature(ctx context.Context, userID string, img []byte) error {
	if img == nil {
		_, err := s.db.Exec(ctx, `DELETE FROM sign_my_signatures WHERE user_id::text = $1`, userID)
		return err
	}
	_, err := s.db.Exec(ctx, `INSERT INTO sign_my_signatures (user_id, image) VALUES ($1::uuid, $2)
		ON CONFLICT (user_id) DO UPDATE SET image = excluded.image, updated_at = now()`, userID, img)
	return err
}

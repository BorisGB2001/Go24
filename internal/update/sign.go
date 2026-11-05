package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
)

// ErrNoKey: this build has no public key of the releases, nothing can be verified (and so installed).
var ErrNoKey = errors.New("update: no release key in this build")

// ErrSignature: the archive is not signed by the key of the releases (another key, damaged archive or signature).
var ErrSignature = errors.New("update: bad signature")

// Verify checks sig — the base64 ed25519 signature of data (the .sig file next to the archive) — against the base64
// public key pub.
func Verify(pub string, data, sig []byte) error {
	if pub == "" {
		return ErrNoKey
	}
	key, err := base64.StdEncoding.DecodeString(pub)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return ErrNoKey
	}
	raw, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return ErrSignature
	}
	if !ed25519.Verify(ed25519.PublicKey(key), data, raw) {
		return ErrSignature
	}
	return nil
}

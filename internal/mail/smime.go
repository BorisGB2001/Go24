package mail

import (
	"bufio"
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/textproto"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/smime"
)

// S/MIME, as Outlook's «Центр управления безопасностью → Защита электронной почты»: the user brings a certificate
// of an address (a .pfx/.p12 file with its password, or PEM) and signs letters with it, encrypts them to people whose
// certificates go24 knows — learnt from their signed letters, or go24's colleagues' own — and to the user (to read the
// copy in «Отправленные»). An incoming signature is checked as the letter arrives (verified, not checked against a
// trusted authority, from someone else than the sender, or the letter changed), and a letter encrypted to the user is
// decrypted with the user's key; go24 keeps the letter as it reads, the server keeps it as it came.

// Cert is one of the user's certificates.
type Cert struct {
	ID, Email, Subject, Issuer, SHA1 string
	NotAfter                         time.Time
}

// Expired: past its date (it still decrypts the letters sent to it before).
func (c Cert) Expired() bool { return time.Now().After(c.NotAfter) }

// Security is what new letters get by default: «Подписывать», «Шифровать».
type Security struct{ Sign, Encrypt bool }

// The states of a letter's signature and encryption (mail_messages.smime_sign, smime_enc).
const (
	SignOK        = "ok"        // the signature holds and its certificate goes to a trusted authority
	SignUntrusted = "untrusted" // the signature holds, its certificate is not known to be trusted (one's own CA)
	SignOther     = "other"     // the signature holds, but its certificate is not the sender's address
	SignBad       = "bad"       // the letter was changed after it was signed
	SignError     = "error"     // the signature cannot be checked
	EncYes        = "yes"       // it came encrypted, go24 decrypted it
	EncNoKey      = "nokey"     // it came encrypted to a certificate the user has not given go24
)

// maxCertFile bounds a certificate file.
const maxCertFile = 64 << 10

// certEmails are the addresses a certificate is of: its e-mail names, else the emailAddress of its subject.
func certEmails(c *x509.Certificate) []string {
	var out []string
	for _, e := range c.EmailAddresses {
		out = append(out, strings.ToLower(e))
	}
	if len(out) == 0 {
		for _, n := range c.Subject.Names {
			if n.Type.Equal(asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 1}) {
				if s, ok := n.Value.(string); ok {
					out = append(out, strings.ToLower(s))
				}
			}
		}
	}
	return out
}

// certOf: whose a certificate is, as shown — «Name <address>».
func certOf(c *x509.Certificate) string {
	emails := certEmails(c)
	name := c.Subject.CommonName
	if len(emails) == 0 {
		return name
	}
	if name == "" || strings.EqualFold(name, emails[0]) {
		return emails[0]
	}
	return name + " <" + emails[0] + ">"
}

// Certs are the user's certificates, the newest first.
func (s *Store) Certs(ctx context.Context, userID string) ([]Cert, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, email, subject, issuer, sha1, not_after FROM mail_smime_certs WHERE user_id::text = $1
		ORDER BY not_after DESC, created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Cert])
}

// AddCert keeps the user's certificate from a .pfx/.p12 file (with its password) or PEM.
func (s *Store) AddCert(ctx context.Context, userID string, data []byte, password string) (Cert, error) {
	if len(data) == 0 || len(data) > maxCertFile {
		return Cert{}, i18n.E("err.mail_smime_file")
	}
	var id smime.Identity
	var err error
	if bytes.Contains(data, []byte("-----BEGIN")) {
		id, err = smime.ParsePEM(data)
	} else {
		id, err = smime.ParsePFX(data, password)
	}
	switch {
	case errors.Is(err, smime.ErrPassword):
		return Cert{}, i18n.E("err.mail_smime_password")
	case errors.Is(err, smime.ErrNoKey):
		return Cert{}, i18n.E("err.mail_smime_nokey")
	case err != nil:
		return Cert{}, i18n.E("err.mail_smime_file")
	}
	emails := certEmails(id.Cert)
	if len(emails) == 0 {
		return Cert{}, i18n.E("err.mail_smime_noemail")
	}
	if time.Now().After(id.Cert.NotAfter) {
		return Cert{}, i18n.E("err.mail_smime_expired")
	}
	key, err := x509.MarshalPKCS8PrivateKey(id.Key)
	if err != nil {
		return Cert{}, i18n.E("err.mail_smime_file")
	}
	sealed, err := s.seal(string(key))
	if err != nil {
		return Cert{}, err
	}
	sum := sha1.Sum(id.Cert.Raw)
	c := Cert{Email: emails[0], Subject: clip(id.Cert.Subject.String(), 500), Issuer: clip(id.Cert.Issuer.String(), 500),
		SHA1: strings.ToUpper(hex.EncodeToString(sum[:])), NotAfter: id.Cert.NotAfter}
	chain := [][]byte{}
	for _, x := range id.Chain {
		chain = append(chain, x.Raw)
	}
	err = s.db.QueryRow(ctx, `INSERT INTO mail_smime_certs (user_id, email, subject, issuer, not_after, sha1, cert, chain, key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (user_id, sha1) DO UPDATE SET key = EXCLUDED.key, chain = EXCLUDED.chain RETURNING id::text`,
		userID, c.Email, c.Subject, c.Issuer, c.NotAfter, c.SHA1, id.Cert.Raw, chain, sealed).Scan(&c.ID)
	return c, err
}

// DeleteCert forgets a certificate of the user (letters encrypted to it cannot be read in go24 any more).
func (s *Store) DeleteCert(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM mail_smime_certs WHERE id::text = $1 AND user_id::text = $2`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// Security: the user's defaults for new letters.
func (s *Store) Security(ctx context.Context, userID string) (Security, error) {
	var x Security
	err := s.db.QueryRow(ctx, `SELECT smime_sign, smime_encrypt FROM mail_settings WHERE user_id::text = $1`, userID).Scan(&x.Sign, &x.Encrypt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return x, err
}

// SaveSecurity keeps them.
func (s *Store) SaveSecurity(ctx context.Context, userID string, x Security) error {
	_, err := s.db.Exec(ctx, `INSERT INTO mail_settings (user_id, smime_sign, smime_encrypt) VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET smime_sign = EXCLUDED.smime_sign, smime_encrypt = EXCLUDED.smime_encrypt`, userID, x.Sign, x.Encrypt)
	return err
}

// identities are the user's certificates with their keys: of the address (the valid ones, the newest first), or
// all of them (email "" — to decrypt with, the expired too).
func (s *Store) identities(ctx context.Context, userID, email string) ([]smime.Identity, error) {
	q := `SELECT cert, chain, key FROM mail_smime_certs WHERE user_id::text = $1`
	args := []any{userID}
	if email != "" {
		q += ` AND email = $2 AND not_after > now()`
		args = append(args, strings.ToLower(email))
	}
	rows, err := s.db.Query(ctx, q+` ORDER BY not_after DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []smime.Identity
	for rows.Next() {
		var der, sealed []byte
		var chain [][]byte
		if err := rows.Scan(&der, &chain, &sealed); err != nil {
			return nil, err
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			continue
		}
		plain, err := s.open(sealed)
		if err != nil {
			continue
		}
		k, err := x509.ParsePKCS8PrivateKey([]byte(plain))
		if err != nil {
			continue
		}
		signer, ok := k.(crypto.Signer)
		if !ok {
			continue
		}
		id := smime.Identity{Cert: cert, Key: signer}
		for _, c := range chain {
			if x, err := x509.ParseCertificate(c); err == nil {
				id.Chain = append(id.Chain, x)
			}
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// HasCert: can the user sign letters from the address?
func (s *Store) HasCert(ctx context.Context, userID, email string) bool {
	var ok bool
	_ = s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mail_smime_certs WHERE user_id::text = $1 AND email = lower($2) AND not_after > now())`,
		userID, email).Scan(&ok)
	return ok
}

// peerCerts are the certificates to encrypt to, by address: learnt by the user from signed letters, else the
// colleague's own in go24 (as Outlook's global address list).
func (s *Store) peerCerts(ctx context.Context, userID string, emails []string) (map[string]*x509.Certificate, error) {
	rows, err := s.db.Query(ctx, `SELECT email, cert FROM (
			SELECT email, cert, 0 AS o, not_after FROM mail_smime_peers WHERE user_id::text = $1 AND email = ANY($2) AND not_after > now()
			UNION ALL SELECT email, cert, 1, not_after FROM mail_smime_certs WHERE email = ANY($2) AND not_after > now()) c
		ORDER BY o DESC, not_after`, userID, emails)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*x509.Certificate{}
	for rows.Next() { // the last one wins: the user's own knowledge, the latest
		var email string
		var der []byte
		if err := rows.Scan(&email, &der); err != nil {
			return nil, err
		}
		if c, err := x509.ParseCertificate(der); err == nil {
			if _, ok := c.PublicKey.(*rsa.PublicKey); ok { // only RSA keys take an encrypted letter
				out[email] = c
			}
		}
	}
	return out, rows.Err()
}

// learnPeer keeps the certificate of someone who signed a letter to the user, for encrypting to them.
func (s *Store) learnPeer(ctx context.Context, userID string, c *x509.Certificate) {
	for _, email := range certEmails(c) {
		_, _ = s.db.Exec(ctx, `INSERT INTO mail_smime_peers (user_id, email, cert, not_after) VALUES ($1, $2, $3, $4)
			ON CONFLICT (user_id, email) DO UPDATE SET cert = EXCLUDED.cert, not_after = EXCLUDED.not_after, seen_at = now()
			WHERE mail_smime_peers.not_after <= EXCLUDED.not_after`, userID, email, c.Raw, c.NotAfter)
	}
}

// maxRedecrypt bounds the letters opened again at once (each is brought from its server).
const maxRedecrypt = 200

// Redecrypt opens the user's letters that came encrypted to a certificate go24 had not (EncNoKey): once the
// certificate is added, as Outlook reads them when the key is in the store. Each is brought from its server again,
// its text, files and signature replace what was kept; a letter still not for the user's keys stays as it is.
// Returns how many it opened.
func (s *Store) Redecrypt(ctx context.Context, userID string) (int, error) {
	rows, err := s.db.Query(ctx, `SELECT x.id::text, x.from_addr, m.email FROM mail_messages x JOIN mailboxes m ON m.id = x.mailbox_id
		WHERE m.user_id::text = $1 AND x.smime_enc = $2 ORDER BY x.sent_at DESC LIMIT $3`, userID, EncNoKey, maxRedecrypt)
	if err != nil {
		return 0, err
	}
	type locked struct{ id, from, own string }
	var list []locked
	for rows.Next() {
		var l locked
		if err := rows.Scan(&l.id, &l.from, &l.own); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, l := range list {
		raw, err := s.Source(ctx, userID, l.id)
		if err != nil {
			continue // gone from the server, or go24's own copy
		}
		raw, sec := s.unsecure(ctx, userID, raw)
		if sec.enc != EncYes {
			continue
		}
		if sec.of(l.from); sec.cert != nil && sec.sign != SignOther && !strings.EqualFold(l.from, l.own) {
			s.learnPeer(ctx, userID, sec.cert)
		}
		p := parseFull(bytes.NewReader(raw))
		if _, err := s.db.Exec(ctx, `UPDATE mail_messages SET body = $2, body_html = $3, ics = $4, voting = $5, vote = $6,
			smime_sign = $7, smime_signer = $8, smime_enc = $9 WHERE id = $1`,
			l.id, p.body, p.html, p.ics, p.voting, p.vote, sec.sign, sec.signer, sec.enc); err != nil {
			return n, err
		}
		if s.Keep != nil { // smime.p7m kept as the letter's file gives way to the files inside
			if _, err := s.db.Exec(ctx, `DELETE FROM files WHERE mail_id = $1`, l.id); err != nil {
				return n, err
			}
			for _, f := range p.files {
				if err := s.Keep(ctx, userID, l.id, f); err != nil {
					return n, err
				}
			}
		}
		n++
	}
	return n, nil
}

// ---------- incoming ----------

// secured is what S/MIME a letter came with.
type secured struct {
	sign, signer, enc string
	cert              *x509.Certificate // the signer's, when the signature holds
}

// unsecure opens a letter's S/MIME: decrypts it with the user's keys, checks a signature and leaves the signed
// letter without it. The source returned is the letter as it reads (its own headers, the content inside).
func (s *Store) unsecure(ctx context.Context, userID string, raw []byte) ([]byte, secured) {
	var sec secured
	var ids []smime.Identity
	for depth := 0; depth < 3; depth++ { // signed then encrypted, encrypted then signed
		head, body, ok := splitHeader(raw)
		if !ok {
			break
		}
		h, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(head))).ReadMIMEHeader()
		if err != nil && len(h) == 0 {
			break
		}
		ct, params, _ := mime.ParseMediaType(h.Get("Content-Type"))
		var inner []byte
		switch {
		case ct == "application/pkcs7-mime" || ct == "application/x-pkcs7-mime":
			p7 := decodeCTE(h.Get("Content-Transfer-Encoding"), body)
			if smime.IsEnveloped(p7) {
				if ids == nil {
					if ids, err = s.identities(ctx, userID, ""); err != nil || len(ids) == 0 {
						sec.enc = EncNoKey
						return raw, sec
					}
				}
				for _, id := range ids {
					d, ok := id.Key.(crypto.Decrypter)
					if !ok {
						continue
					}
					if inner, err = smime.Decrypt(p7, id.Cert, d); err == nil {
						break
					}
				}
				if inner == nil {
					sec.enc = EncNoKey
					return raw, sec
				}
				sec.enc = EncYes
			} else if smime.IsSigned(p7) {
				v, err := smime.Verify(p7, nil)
				if err != nil {
					sec.sign = signState(err)
					return raw, sec // the content of a letter whose signature is broken is not trusted out of it
				}
				sec.signed(v)
				inner = v.Content
			} else {
				return raw, sec
			}
		case ct == "multipart/signed" && strings.Contains(strings.ToLower(params["protocol"]), "pkcs7-signature"):
			content, sig, ok := signedParts(body, params["boundary"])
			if !ok {
				sec.sign = SignError
				return raw, sec
			}
			v, err := smime.Verify(sig, content)
			if err != nil {
				sec.sign = signState(err)
			} else {
				sec.signed(v)
			}
			inner = content // a changed letter is shown too (as Outlook, with the warning)
		default:
			return raw, sec
		}
		raw = append(stripContent(head), inner...)
	}
	return raw, sec
}

func signState(err error) string {
	if errors.Is(err, smime.ErrBadSignature) {
		return SignBad
	}
	return SignError
}

// signed records a signature that holds: whether its certificate is trusted.
func (sec *secured) signed(v *smime.Signed) {
	sec.cert, sec.signer = v.Signer, clip(certOf(v.Signer), 300)
	sec.sign = SignOK
	if v.Trust(nil) != nil {
		sec.sign = SignUntrusted
	}
}

// of checks the signer against the letter's sender.
func (sec *secured) of(from string) {
	if sec.cert == nil || from == "" {
		return
	}
	for _, e := range certEmails(sec.cert) {
		if strings.EqualFold(e, from) {
			return
		}
	}
	sec.sign = SignOther
}

// splitHeader splits an entity into its header (with the line ending it) and its body.
func splitHeader(raw []byte) (head, body []byte, ok bool) {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 && (bytes.Index(raw, []byte("\n\n")) < 0 || bytes.Index(raw, []byte("\n\n")) > i) {
		return raw[:i+2], raw[i+4:], true
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		return raw[:i+1], raw[i+2:], true
	}
	return nil, nil, false
}

// stripContent drops the Content-* fields of a header (the content inside brings its own) — the letter's From,
// To, Subject… stay.
func stripContent(head []byte) []byte {
	var out bytes.Buffer
	skip := false
	for _, line := range bytes.SplitAfter(head, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if !skip {
				out.Write(line)
			}
			continue
		}
		name, _, _ := bytes.Cut(line, []byte(":"))
		skip = bytes.HasPrefix(bytes.ToLower(bytes.TrimSpace(name)), []byte("content-"))
		if !skip {
			out.Write(line)
		}
	}
	return out.Bytes()
}

func decodeCTE(cte string, body []byte) []byte {
	if strings.EqualFold(strings.TrimSpace(cte), "base64") {
		b, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, &stripSpace{r: bytes.NewReader(body)}))
		return b
	}
	return body
}

// stripSpace reads base64 without its line breaks (and stray spaces).
type stripSpace struct{ r io.Reader }

func (s *stripSpace) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	j := 0
	for _, c := range p[:n] {
		if c != '\r' && c != '\n' && c != ' ' && c != '\t' {
			p[j] = c
			j++
		}
	}
	return j, err
}

// signedParts are the two parts of a multipart/signed body (RFC 1847): the content exactly as it was signed (its
// bytes between the boundaries) and the signature decoded.
func signedParts(body []byte, boundary string) (content, sig []byte, ok bool) {
	if boundary == "" {
		return nil, nil, false
	}
	delim := []byte("--" + boundary)
	var parts [][]byte
	rest := body
	if !bytes.HasPrefix(rest, delim) {
		i := bytes.Index(rest, append([]byte("\n"), delim...))
		if i < 0 {
			return nil, nil, false
		}
		rest = rest[i+1:]
	}
	for len(parts) < 2 {
		rest = rest[len(delim):]
		if bytes.HasPrefix(rest, []byte("--")) {
			break
		}
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			rest = rest[i+1:]
		} else {
			return nil, nil, false
		}
		i := bytes.Index(rest, append([]byte("\n"), delim...))
		if i < 0 {
			return nil, nil, false
		}
		p := rest[:i]
		p = bytes.TrimSuffix(p, []byte("\r")) // the line break before a boundary belongs to the boundary
		parts = append(parts, p)
		rest = rest[i+1:]
	}
	if len(parts) != 2 {
		return nil, nil, false
	}
	head, b, ok := splitHeader(parts[1])
	if !ok {
		return nil, nil, false
	}
	h, _ := textproto.NewReader(bufio.NewReader(bytes.NewReader(head))).ReadMIMEHeader()
	return parts[0], decodeCTE(h.Get("Content-Transfer-Encoding"), b), true
}

// ---------- outgoing ----------

// secure signs and (or) encrypts a composed letter: its own header stays, its content (the entity from its
// Content-Type on) becomes multipart/signed and (or) application/pkcs7-mime. The copies (the letter, its copy for
// «Отправленные») share the content.
func (s *Store) secure(ctx context.Context, userID string, from string, rcpts []string, l Letter, now time.Time, msgs ...[]byte) ([][]byte, secured, error) {
	var sec secured
	ids, err := s.identities(ctx, userID, from)
	if err != nil {
		return nil, sec, err
	}
	if len(ids) == 0 {
		return nil, sec, i18n.E("err.mail_smime_nocert", from)
	}
	me := ids[0]
	_, entity := splitEntity(msgs[0])
	if l.Sign {
		p7, err := smime.Sign(entity, me.Cert, me.Chain, me.Key, now)
		if err != nil {
			return nil, sec, err
		}
		b := fmt.Sprintf("----=_go24s_%d", now.UnixNano())
		var e bytes.Buffer
		fmt.Fprintf(&e, "Content-Type: multipart/signed; protocol=\"application/pkcs7-signature\"; micalg=sha-256;\r\n\tboundary=%q\r\n\r\n", b)
		fmt.Fprintf(&e, "This is a cryptographically signed message in MIME format.\r\n\r\n--%s\r\n", b)
		e.Write(entity)
		fmt.Fprintf(&e, "\r\n--%s\r\nContent-Type: application/pkcs7-signature; name=\"smime.p7s\"\r\nContent-Transfer-Encoding: base64\r\n"+
			"Content-Disposition: attachment; filename=\"smime.p7s\"\r\nContent-Description: S/MIME Cryptographic Signature\r\n\r\n", b)
		e.Write(base64Lines(p7))
		fmt.Fprintf(&e, "\r\n--%s--\r\n", b)
		entity = e.Bytes()
		sec.signed(&smime.Signed{Signer: me.Cert, Certs: me.Chain, Time: now})
	}
	if l.Encrypt {
		lower := make([]string, len(rcpts))
		for i, r := range rcpts {
			lower[i] = strings.ToLower(r)
		}
		peers, err := s.peerCerts(ctx, userID, lower)
		if err != nil {
			return nil, sec, err
		}
		to := []*x509.Certificate{me.Cert}
		var missing []string
		for _, r := range lower {
			if c, ok := peers[r]; ok {
				to = append(to, c)
			} else if !strings.EqualFold(r, from) {
				missing = append(missing, r)
			}
		}
		if len(missing) > 0 {
			return nil, sec, i18n.E("err.mail_smime_peers", strings.Join(missing, ", "))
		}
		p7, err := smime.Encrypt(entity, to)
		if err != nil {
			return nil, sec, i18n.E("err.mail_smime_peers", err.Error())
		}
		var e bytes.Buffer
		e.WriteString("Content-Type: application/pkcs7-mime; smime-type=enveloped-data; name=\"smime.p7m\"\r\nContent-Transfer-Encoding: base64\r\n" +
			"Content-Disposition: attachment; filename=\"smime.p7m\"\r\nContent-Description: S/MIME Encrypted Message\r\n\r\n")
		e.Write(base64Lines(p7))
		entity = e.Bytes()
		sec.enc = EncYes
	}
	out := make([][]byte, len(msgs))
	for i, m := range msgs {
		top, _ := splitEntity(m)
		out[i] = append(append([]byte(nil), top...), entity...)
	}
	return out, sec, nil
}

// splitEntity splits a letter compose made into its own header and its content: compose writes Content-Type
// (and Content-Transfer-Encoding) last.
func splitEntity(msg []byte) (top, entity []byte) {
	end := bytes.Index(msg, []byte("\r\n\r\n"))
	i := bytes.LastIndex(msg[:end+2], []byte("\r\nContent-Type: "))
	return msg[:i+2], msg[i+2:]
}

func base64Lines(b []byte) []byte {
	enc := base64.StdEncoding.EncodeToString(b)
	var out bytes.Buffer
	for len(enc) > 76 {
		out.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	out.WriteString(enc + "\r\n")
	return out.Bytes()
}

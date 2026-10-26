// Package smime is S/MIME of the mail (RFC 8551) without outside libraries: CMS signed and enveloped data
// (RFC 5652) — signing a letter, checking a signature, encrypting a letter to its recipients' certificates and
// decrypting one sent to the user — and reading the user's certificate from a .pfx/.p12 file (PKCS #12, RFC 7292).
// Signatures: RSA (PKCS #1 v1.5, as Outlook) and ECDSA, SHA-1…SHA-512 read, SHA-256 written. Encryption: RSA key
// transport (PKCS #1 v1.5, OAEP read), AES-CBC (AES-256 written) and 3DES read.
package smime

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"hash"
	"math/big"
	"time"
)

var (
	oidData          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidSignedData    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidEnvelopedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 3}
	oidContentType   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	oidMessageDigest = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidSigningTime   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 5}
	oidCapabilities  = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 15}

	oidSHA1   = asn1.ObjectIdentifier{1, 3, 14, 3, 2, 26}
	oidSHA256 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidSHA384 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 2}
	oidSHA512 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 3}

	oidRSA       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
	oidRSASHA1   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 5}
	oidRSASHA256 = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 11}
	oidRSASHA384 = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 12}
	oidRSASHA512 = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 13}
	oidRSAOAEP   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 7}
	oidECKey     = asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}
	oidECSHA1    = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 1}
	oidECSHA256  = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 2}
	oidECSHA384  = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 3}
	oidECSHA512  = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 4}

	oidAES128 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 2}
	oidAES192 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 22}
	oidAES256 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 42}
	oid3DES   = asn1.ObjectIdentifier{1, 2, 840, 113549, 3, 7}
)

var (
	// ErrBadSignature: the signature does not fit the letter — it was changed on the way (or signed wrong).
	ErrBadSignature = errors.New("smime: the signature does not match")
	// ErrNotForMe: the letter is encrypted, but not to this certificate.
	ErrNotForMe = errors.New("smime: not encrypted to this certificate")
	// ErrUnsupported: an algorithm go24 does not know.
	ErrUnsupported = errors.New("smime: unsupported algorithm")
	// ErrKey: a key that cannot sign or take an encrypted letter (not RSA for encryption).
	ErrKey = errors.New("smime: unsupported key")
)

func marshal(v any) []byte {
	b, err := asn1.Marshal(v)
	if err != nil {
		panic(err) // only go24's own values
	}
	return b
}

func seq(parts ...[]byte) []byte  { return tlv(0, true, asn1.TagSequence, bytes.Join(parts, nil)) }
func set(parts ...[]byte) []byte  { return tlv(0, true, asn1.TagSet, bytes.Join(parts, nil)) }
func ctx0(parts ...[]byte) []byte { return tlv(2, true, 0, bytes.Join(parts, nil)) }

var asnNull = []byte{5, 0}

func alg(id asn1.ObjectIdentifier, params ...[]byte) []byte {
	return seq(append([][]byte{marshal(id)}, params...)...)
}

// issuerSerial is the IssuerAndSerialNumber of a certificate.
func issuerSerial(c *x509.Certificate) []byte { return seq(c.RawIssuer, marshal(c.SerialNumber)) }

// isCert: the signer or recipient identifier names this certificate (issuer and serial, or [0] its key identifier).
func isCert(id node, c *x509.Certificate) bool {
	if id.is(2, 0) {
		return len(c.SubjectKeyId) > 0 && bytes.Equal(id.bytes(), c.SubjectKeyId)
	}
	if !id.seq() || len(id.kids) != 2 {
		return false
	}
	var serial *big.Int
	if _, err := asn1.Unmarshal(id.kid(1).full, &serial); err != nil {
		return false
	}
	return bytes.Equal(id.kid(0).der(), c.RawIssuer) && serial.Cmp(c.SerialNumber) == 0
}

func hashOf(id asn1.ObjectIdentifier) (crypto.Hash, func() hash.Hash) {
	switch {
	case id.Equal(oidSHA1):
		return crypto.SHA1, sha1.New
	case id.Equal(oidSHA256):
		return crypto.SHA256, sha256.New
	case id.Equal(oidSHA384):
		return crypto.SHA384, sha512.New384
	case id.Equal(oidSHA512):
		return crypto.SHA512, sha512.New
	}
	return 0, nil
}

// Sign signs the content (the MIME entity of a letter, CRLF) with the user's certificate: a detached
// SignedData with the certificate (and the rest of its chain), the content type, the time and the digest signed, and
// the ciphers the user takes (smimeCapabilities: AES-256, AES-128), as Outlook.
func Sign(content []byte, cert *x509.Certificate, chain []*x509.Certificate, key crypto.Signer, now time.Time) ([]byte, error) {
	var sigAlg asn1.ObjectIdentifier
	switch key.Public().(type) {
	case *rsa.PublicKey:
		sigAlg = oidRSA
	case *ecdsa.PublicKey:
		sigAlg = oidECSHA256
	default:
		return nil, ErrKey
	}
	digest := sha256.Sum256(content)
	attr := func(id asn1.ObjectIdentifier, value []byte) []byte { return seq(marshal(id), set(value)) }
	caps := seq(alg(oidAES256), alg(oidAES128))
	attrs := [][]byte{attr(oidContentType, marshal(oidData)), attr(oidSigningTime, marshal(now.UTC())),
		attr(oidMessageDigest, marshal(digest[:])), attr(oidCapabilities, caps)}
	sortDER(attrs)
	signed := set(attrs...)
	h := sha256.Sum256(signed)
	sig, err := key.Sign(rand.Reader, h[:], crypto.SHA256)
	if err != nil {
		return nil, err
	}
	sigAlgID := alg(sigAlg)
	if sigAlg.Equal(oidRSA) {
		sigAlgID = alg(sigAlg, asnNull)
	}
	signerInfo := seq(marshal(1), issuerSerial(cert), alg(oidSHA256), tlv(2, true, 0, bytes.Join(attrs, nil)), sigAlgID, marshal(sig))
	var certs [][]byte
	for _, c := range append([]*x509.Certificate{cert}, chain...) {
		certs = append(certs, c.Raw)
	}
	sd := seq(marshal(1), set(alg(oidSHA256)), seq(marshal(oidData)), tlv(2, true, 0, bytes.Join(certs, nil)), set(signerInfo))
	return seq(marshal(oidSignedData), ctx0(sd)), nil
}

// sortDER sorts the encodings of a SET OF, as DER wants.
func sortDER(items [][]byte) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && bytes.Compare(items[j], items[j-1]) < 0; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

// Signed is a checked signature.
type Signed struct {
	Content []byte              // what was signed (the detached content, or the one carried inside)
	Signer  *x509.Certificate   // whose signature it is
	Certs   []*x509.Certificate // the certificates it carries (the signer's chain)
	Time    time.Time           // the signing time it states (zero — none)
}

// contentInfo reads a ContentInfo of the given type: its [0] content.
func contentInfo(p7 []byte, typ asn1.ObjectIdentifier) (node, error) {
	ci, _, err := parseBER(p7)
	if err != nil {
		return node{}, err
	}
	if !ci.seq() || !ci.kid(0).oid().Equal(typ) || !ci.kid(1).is(2, 0) || len(ci.kid(1).kids) != 1 {
		return node{}, errBER
	}
	return ci.kid(1).kid(0), nil
}

// IsSigned and IsEnveloped tell what a pkcs7-mime part holds when its smime-type does not say.
func IsSigned(p7 []byte) bool { _, err := contentInfo(p7, oidSignedData); return err == nil }

func IsEnveloped(p7 []byte) bool { _, err := contentInfo(p7, oidEnvelopedData); return err == nil }

// Verify checks a SignedData: of the detached content (a multipart/signed letter), or of the content inside
// (detached nil — an application/pkcs7-mime signed-data letter). ErrBadSignature — the letter was changed.
// Whether the signer is trusted is Trust.
func Verify(p7, detached []byte) (*Signed, error) {
	sd, err := contentInfo(p7, oidSignedData)
	if err != nil {
		return nil, err
	}
	if !sd.seq() || len(sd.kids) < 4 {
		return nil, errBER
	}
	out := &Signed{Content: detached}
	encap := sd.kid(2)
	if out.Content == nil {
		if !encap.kid(1).is(2, 0) {
			return nil, errBER
		}
		out.Content = encap.kid(1).kid(0).bytes()
	}
	var infos node
	for _, k := range sd.kids[3:] {
		switch {
		case k.is(2, 0): // certificates
			for _, c := range k.kids {
				if cert, err := x509.ParseCertificate(c.der()); err == nil {
					out.Certs = append(out.Certs, cert)
				}
			}
		case k.is(0, asn1.TagSet):
			infos = k
		}
	}
	if len(infos.kids) == 0 {
		return nil, errBER
	}
	var first error
	for _, si := range infos.kids { // the first signer whose signature holds; else why not — a changed letter first
		e := out.check(si)
		if e == nil {
			return out, nil
		}
		if first == nil || errors.Is(e, ErrBadSignature) {
			first = e
		}
	}
	return nil, first
}

// check checks one SignerInfo.
func (out *Signed) check(si node) error {
	if !si.seq() || len(si.kids) < 5 {
		return errBER
	}
	var cert *x509.Certificate
	for _, c := range out.Certs {
		if isCert(si.kid(1), c) {
			cert = c
		}
	}
	if cert == nil {
		return x509.UnknownAuthorityError{}
	}
	h, newHash := hashOf(si.kid(2).kid(0).oid())
	if newHash == nil {
		return ErrUnsupported
	}
	i := 3
	signed := out.Content
	if a := si.kid(i); a.is(2, 0) { // signed attributes: the digest of the content is among them
		i++
		var digest []byte
		for _, at := range a.kids {
			switch id := at.kid(0).oid(); {
			case id.Equal(oidMessageDigest):
				digest = at.kid(1).kid(0).bytes()
			case id.Equal(oidSigningTime):
				var t time.Time
				if _, err := asn1.Unmarshal(at.kid(1).kid(0).der(), &t); err == nil {
					out.Time = t
				}
			}
		}
		d := newHash()
		d.Write(out.Content)
		if !bytes.Equal(d.Sum(nil), digest) {
			return ErrBadSignature
		}
		// they are signed as a SET (RFC 5652 5.4), sent as [0]
		var body []byte
		for _, k := range a.kids {
			body = append(body, k.der()...)
		}
		signed = tlv(0, true, asn1.TagSet, body)
	}
	sigAlg, sig := si.kid(i).kid(0).oid(), si.kid(i+1).bytes()
	algo := x509.UnknownSignatureAlgorithm
	rsaKey := sigAlg.Equal(oidRSA) || sigAlg.Equal(oidRSASHA1) || sigAlg.Equal(oidRSASHA256) || sigAlg.Equal(oidRSASHA384) || sigAlg.Equal(oidRSASHA512)
	ecKey := sigAlg.Equal(oidECKey) || sigAlg.Equal(oidECSHA1) || sigAlg.Equal(oidECSHA256) || sigAlg.Equal(oidECSHA384) || sigAlg.Equal(oidECSHA512)
	switch {
	case rsaKey && h == crypto.SHA1:
		algo = x509.SHA1WithRSA
	case rsaKey && h == crypto.SHA256:
		algo = x509.SHA256WithRSA
	case rsaKey && h == crypto.SHA384:
		algo = x509.SHA384WithRSA
	case rsaKey && h == crypto.SHA512:
		algo = x509.SHA512WithRSA
	case ecKey && h == crypto.SHA1:
		algo = x509.ECDSAWithSHA1
	case ecKey && h == crypto.SHA256:
		algo = x509.ECDSAWithSHA256
	case ecKey && h == crypto.SHA384:
		algo = x509.ECDSAWithSHA384
	case ecKey && h == crypto.SHA512:
		algo = x509.ECDSAWithSHA512
	default:
		return ErrUnsupported
	}
	if algo == x509.SHA1WithRSA || algo == x509.ECDSAWithSHA1 { // CheckSignature refuses SHA-1; old letters still use it
		if err := checkSHA1(cert, algo, signed, sig); err != nil {
			return err
		}
	} else if err := cert.CheckSignature(algo, signed, sig); err != nil {
		return ErrBadSignature
	}
	out.Signer = cert
	return nil
}

func checkSHA1(cert *x509.Certificate, algo x509.SignatureAlgorithm, signed, sig []byte) error {
	d := sha1.Sum(signed)
	ok := false
	switch k := cert.PublicKey.(type) {
	case *rsa.PublicKey:
		ok = algo == x509.SHA1WithRSA && rsa.VerifyPKCS1v15(k, crypto.SHA1, d[:], sig) == nil
	case *ecdsa.PublicKey:
		ok = algo == x509.ECDSAWithSHA1 && ecdsa.VerifyASN1(k, d[:], sig)
	}
	if !ok {
		return ErrBadSignature
	}
	return nil
}

// Trust checks the signer's certificate: a chain to the roots (the system's when nil) through the certificates
// the letter carries, valid at the signing time (now when it states none), for e-mail protection.
func (s *Signed) Trust(roots *x509.CertPool) error {
	if s.Signer == nil {
		return x509.UnknownAuthorityError{}
	}
	inter := x509.NewCertPool()
	for _, c := range s.Certs {
		if c != s.Signer {
			inter.AddCert(c)
		}
	}
	at := s.Time
	if at.IsZero() {
		at = time.Now()
	}
	_, err := s.Signer.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: at,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}})
	return err
}

// Encrypt encrypts the content (a MIME entity) to the recipients' certificates (RSA keys): EnvelopedData,
// AES-256-CBC, the key for each recipient by PKCS #1 v1.5 — what Outlook, Thunderbird and Apple Mail all read.
func Encrypt(content []byte, recipients []*x509.Certificate) ([]byte, error) {
	key := make([]byte, 32)
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	var infos [][]byte
	for _, c := range recipients {
		pub, ok := c.PublicKey.(*rsa.PublicKey)
		if !ok {
			return nil, ErrKey
		}
		ek, err := rsa.EncryptPKCS1v15(rand.Reader, pub, key)
		if err != nil {
			return nil, err
		}
		infos = append(infos, seq(marshal(0), issuerSerial(c), alg(oidRSA, asnNull), marshal(ek)))
	}
	block, _ := aes.NewCipher(key)
	padded := pad(content, aes.BlockSize)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(padded, padded)
	eci := seq(marshal(oidData), alg(oidAES256, marshal(iv)), tlv(2, false, 0, padded))
	ed := seq(marshal(0), set(infos...), eci)
	return seq(marshal(oidEnvelopedData), ctx0(ed)), nil
}

func pad(b []byte, n int) []byte {
	p := n - len(b)%n
	return append(append([]byte(nil), b...), bytes.Repeat([]byte{byte(p)}, p)...)
}

// Decrypt decrypts an EnvelopedData sent to the certificate with its key. ErrNotForMe — encrypted to others.
func Decrypt(p7 []byte, cert *x509.Certificate, key crypto.Decrypter) ([]byte, error) {
	ed, err := contentInfo(p7, oidEnvelopedData)
	if err != nil {
		return nil, err
	}
	i := 1
	if ed.kid(i).is(2, 0) { // originatorInfo
		i++
	}
	infos, eci := ed.kid(i), ed.kid(i+1)
	if !infos.is(0, asn1.TagSet) || !eci.seq() {
		return nil, errBER
	}
	var ek []byte
	var oaep bool
	for _, ri := range infos.kids { // KeyTransRecipientInfo (a SEQUENCE; the others are tagged)
		if ri.seq() && isCert(ri.kid(1), cert) {
			ka := ri.kid(2).kid(0).oid()
			if !ka.Equal(oidRSA) && !ka.Equal(oidRSAOAEP) {
				return nil, ErrUnsupported
			}
			ek, oaep = ri.kid(3).bytes(), ka.Equal(oidRSAOAEP)
			break
		}
	}
	if ek == nil {
		return nil, ErrNotForMe
	}
	var opts crypto.DecrypterOpts
	if oaep {
		opts = &rsa.OAEPOptions{Hash: oaepHash(infos, cert)}
	}
	cek, err := key.Decrypt(rand.Reader, ek, opts)
	if err != nil {
		return nil, err
	}
	ca := eci.kid(1)
	iv := ca.kid(1).bytes()
	var block cipher.Block
	switch id := ca.kid(0).oid(); {
	case id.Equal(oidAES128) || id.Equal(oidAES192) || id.Equal(oidAES256):
		block, err = aes.NewCipher(cek)
	case id.Equal(oid3DES):
		block, err = des.NewTripleDESCipher(cek)
	default:
		return nil, ErrUnsupported
	}
	if err != nil {
		return nil, err
	}
	data := eci.kid(2)
	if !data.is(2, 0) {
		return nil, errBER
	}
	body := append([]byte(nil), data.bytes()...) // not in place: the letter stays as it is
	if len(iv) != block.BlockSize() || len(body) == 0 || len(body)%block.BlockSize() != 0 {
		return nil, errBER
	}
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(body, body)
	p := int(body[len(body)-1])
	if p == 0 || p > block.BlockSize() || p > len(body) {
		return nil, errBER
	}
	return body[:len(body)-p], nil
}

// oaepHash is the hash of an RSAES-OAEP recipient's parameters (SHA-1 — the default — when none).
func oaepHash(infos node, cert *x509.Certificate) crypto.Hash {
	for _, ri := range infos.kids {
		if ri.seq() && isCert(ri.kid(1), cert) {
			params := ri.kid(2).kid(1)
			if p := params.kid(0); p.is(2, 0) {
				if h, _ := hashOf(p.kid(0).kid(0).oid()); h != 0 {
					return h
				}
			}
		}
	}
	return crypto.SHA1
}

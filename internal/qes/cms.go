// Package qes checks a qualified electronic signature (КЭП), as the signer's tool makes it — CryptoPro CSP through
// its browser plug-in: a detached CAdES-BES signature (CMS SignedData) of the document with the signer's certificate.
// GOST R 34.10-2012 with Streebog (256 and 512 bits) is checked here, RSA and ECDSA with SHA-2 — by crypto/x509.
// The certificate's chain to an accredited centre is not checked: what is shown is who the certificate names.
package qes

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"errors"
	"hash"
	"math/big"
	"strings"
	"time"

	"go24/internal/i18n"
)

var (
	ErrFormat    = i18n.E("qes.err.format")
	ErrAlgorithm = i18n.E("qes.err.algorithm")
	ErrDocument  = i18n.E("qes.err.document")
	ErrSignature = i18n.E("qes.err.signature")
	ErrExpired   = i18n.E("qes.err.expired")
)

// Cert is what the signer's certificate says: the owner and their organization, the identifiers of Russian
// qualified certificates (ИНН, ОГРН, СНИЛС), the certificate's number, issuer and term.
type Cert struct {
	Owner     string    `json:"owner"` // CN
	Surname   string    `json:"surname,omitempty"`
	Given     string    `json:"given,omitempty"` // the name and patronymic
	Title     string    `json:"title,omitempty"`
	Org       string    `json:"org,omitempty"`
	INN       string    `json:"inn,omitempty"`    // the person's
	INNLE     string    `json:"inn_le,omitempty"` // the organization's
	OGRN      string    `json:"ogrn,omitempty"`   // ОГРН, or ОГРНИП of an individual entrepreneur
	SNILS     string    `json:"snils,omitempty"`
	Email     string    `json:"email,omitempty"`
	Serial    string    `json:"serial"` // hex
	Issuer    string    `json:"issuer"`
	NotBefore time.Time `json:"not_before"`
	NotAfter  time.Time `json:"not_after"`
	Alg       string    `json:"alg"` // GOST R 34.10-2012 256 | GOST R 34.10-2012 512 | RSA | ECDSA
}

// Signed is a good signature: the certificate and when the signer's tool says it signed.
type Signed struct {
	Cert
	SigningTime *time.Time `json:"signing_time,omitempty"`
}

var (
	oidSignedData    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidMessageDigest = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidSigningTime   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 5}

	oidGOST256    = "1.2.643.7.1.1.1.1" // the keys of GOST R 34.10-2012
	oidGOST512    = "1.2.643.7.1.1.1.2"
	oidStreebog   = map[string]func() hash.Hash{"1.2.643.7.1.1.2.2": NewStreebog256, "1.2.643.7.1.1.2.3": NewStreebog512}
	oidSHA2       = map[string]func() hash.Hash{"2.16.840.1.101.3.4.2.1": sha256.New, "2.16.840.1.101.3.4.2.2": sha512.New384, "2.16.840.1.101.3.4.2.3": sha512.New}
	oidSignatures = map[string]string{ // the signature algorithms of SignerInfo: of which key
		"1.2.643.7.1.1.1.1": oidGOST256, "1.2.643.7.1.1.3.2": oidGOST256,
		"1.2.643.7.1.1.1.2": oidGOST512, "1.2.643.7.1.1.3.3": oidGOST512,
		"1.2.840.113549.1.1.1": "rsa", "1.2.840.113549.1.1.11": "rsa", "1.2.840.113549.1.1.12": "rsa", "1.2.840.113549.1.1.13": "rsa",
		"1.2.840.10045.2.1": "ecdsa", "1.2.840.10045.4.3.2": "ecdsa", "1.2.840.10045.4.3.3": "ecdsa", "1.2.840.10045.4.3.4": "ecdsa",
	}
	x509Algs = map[string]x509.SignatureAlgorithm{
		"rsa 2.16.840.1.101.3.4.2.1": x509.SHA256WithRSA, "rsa 2.16.840.1.101.3.4.2.2": x509.SHA384WithRSA, "rsa 2.16.840.1.101.3.4.2.3": x509.SHA512WithRSA,
		"ecdsa 2.16.840.1.101.3.4.2.1": x509.ECDSAWithSHA256, "ecdsa 2.16.840.1.101.3.4.2.2": x509.ECDSAWithSHA384, "ecdsa 2.16.840.1.101.3.4.2.3": x509.ECDSAWithSHA512,
	}
	// The names of a Russian qualified certificate (Приказ ФСБ № 795).
	oidNames = map[string]func(*Cert, string){
		"2.5.4.3": func(c *Cert, v string) { c.Owner = v }, "2.5.4.4": func(c *Cert, v string) { c.Surname = v },
		"2.5.4.42": func(c *Cert, v string) { c.Given = v }, "2.5.4.12": func(c *Cert, v string) { c.Title = v },
		"2.5.4.10": func(c *Cert, v string) { c.Org = v }, "1.2.643.3.131.1.1": func(c *Cert, v string) { c.INN = v },
		"1.2.643.100.4": func(c *Cert, v string) { c.INNLE = v }, "1.2.643.100.1": func(c *Cert, v string) { c.OGRN = v },
		"1.2.643.100.5": func(c *Cert, v string) { c.OGRN = v }, "1.2.643.100.3": func(c *Cert, v string) { c.SNILS = v },
		"1.2.840.113549.1.9.1": func(c *Cert, v string) { c.Email = v },
	}
)

type contentInfo struct {
	Type    asn1.ObjectIdentifier
	Content asn1.RawValue `asn1:"explicit,tag:0"`
}

type signedData struct {
	Version    int
	DigestAlgs asn1.RawValue
	Encap      struct {
		Type    asn1.ObjectIdentifier
		Content asn1.RawValue `asn1:"optional,explicit,tag:0"`
	}
	Certs   asn1.RawValue `asn1:"optional,tag:0"`
	CRLs    asn1.RawValue `asn1:"optional,tag:1"`
	Signers []signerInfo  `asn1:"set"`
}

type signerInfo struct {
	Version   int
	SID       asn1.RawValue
	DigestAlg pkix.AlgorithmIdentifier
	Attrs     asn1.RawValue `asn1:"optional,tag:0"`
	SigAlg    pkix.AlgorithmIdentifier
	Sig       []byte
	Unsigned  asn1.RawValue `asn1:"optional,tag:1"`
}

type attribute struct {
	Type   asn1.ObjectIdentifier
	Values asn1.RawValue `asn1:"set"`
}

type issuerSerial struct {
	Issuer asn1.RawValue
	Serial *big.Int
}

// Decode is the signature as the browser gives it: base64 (with line breaks, maybe PEM) or binary DER.
func Decode(s []byte) []byte {
	t := string(s)
	if i := strings.Index(t, "-----BEGIN"); i >= 0 { // PEM: the base64 between the lines
		t = t[i:]
		if j := strings.Index(t, "\n"); j >= 0 {
			t = t[j+1:]
		}
		if j := strings.Index(t, "-----END"); j >= 0 {
			t = t[:j]
		}
	}
	t = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, t)
	if b, err := base64.StdEncoding.DecodeString(t); err == nil && len(b) > 0 {
		return b
	}
	return s
}

// Verify checks the detached signature sig of the document doc, signed by one signer: that it is good and of this
// very document, and the certificate was valid when it was signed (the signing time the signature says, else now).
func Verify(sig, doc []byte, now time.Time) (Signed, error) {
	sig, err := der(sig)
	if err != nil {
		return Signed{}, ErrFormat
	}
	var ci contentInfo
	if rest, err := asn1.Unmarshal(sig, &ci); err != nil || len(rest) > 0 || !ci.Type.Equal(oidSignedData) {
		return Signed{}, ErrFormat
	}
	var sd signedData
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil || len(sd.Signers) != 1 {
		return Signed{}, ErrFormat
	}
	if len(sd.Encap.Content.Bytes) > 0 { // an attached signature: of this document only
		in := sd.Encap.Content.Bytes
		if sd.Encap.Content.IsCompound { // an OCTET STRING by parts
			var parts []byte
			for rest := in; len(rest) > 0; {
				var part []byte
				var err error
				if rest, err = asn1.Unmarshal(rest, &part); err != nil {
					return Signed{}, ErrFormat
				}
				parts = append(parts, part...)
			}
			in = parts
		}
		if !bytes.Equal(in, doc) {
			return Signed{}, ErrDocument
		}
	}
	si := sd.Signers[0]
	certs, err := x509.ParseCertificates(sd.Certs.Bytes)
	if err != nil {
		return Signed{}, ErrFormat
	}
	cert := signerCert(certs, si.SID)
	if cert == nil {
		return Signed{}, ErrFormat
	}
	keyAlg, ok := oidSignatures[si.SigAlg.Algorithm.String()]
	newHash := oidStreebog[si.DigestAlg.Algorithm.String()]
	if keyAlg == "rsa" || keyAlg == "ecdsa" {
		newHash = oidSHA2[si.DigestAlg.Algorithm.String()]
	}
	if !ok || newHash == nil {
		return Signed{}, ErrAlgorithm
	}
	h := newHash()
	h.Write(doc)
	digest := h.Sum(nil)

	out := Signed{Cert: certInfo(cert)}
	signed := doc // what the signature is of: the signed attributes (the document's digest among them), else the document
	if len(si.Attrs.FullBytes) > 0 {
		signed = append([]byte{0x31}, si.Attrs.FullBytes[1:]...) // signed as a SET OF, not as [0]
		var attrs []attribute
		if _, err := asn1.UnmarshalWithParams(signed, &attrs, "set"); err != nil {
			return Signed{}, ErrFormat
		}
		var md []byte
		for _, a := range attrs {
			switch {
			case a.Type.Equal(oidMessageDigest):
				_, _ = asn1.Unmarshal(a.Values.Bytes, &md)
			case a.Type.Equal(oidSigningTime):
				var t time.Time
				if _, err := asn1.Unmarshal(a.Values.Bytes, &t); err == nil {
					out.SigningTime = &t
				}
			}
		}
		if !bytes.Equal(md, digest) {
			return Signed{}, ErrDocument
		}
	}

	switch keyAlg {
	case oidGOST256, oidGOST512:
		c, pub, alg, err := gostKey(cert.RawSubjectPublicKeyInfo)
		if err != nil {
			return Signed{}, err
		}
		if alg != keyAlg || (c.size() == 32) != (keyAlg == oidGOST256) {
			return Signed{}, ErrAlgorithm
		}
		h := newHash()
		h.Write(signed)
		if !c.verify(pub, le(h.Sum(nil)), si.Sig) {
			return Signed{}, ErrSignature
		}
	default:
		alg, ok := x509Algs[keyAlg+" "+si.DigestAlg.Algorithm.String()]
		if !ok {
			return Signed{}, ErrAlgorithm
		}
		if err := cert.CheckSignature(alg, signed, si.Sig); err != nil {
			return Signed{}, ErrSignature
		}
	}
	at := now
	if out.SigningTime != nil {
		at = *out.SigningTime
	}
	if at.Before(cert.NotBefore) || at.After(cert.NotAfter) {
		return out, ErrExpired
	}
	return out, nil
}

// signerCert is the certificate the signer names: by its issuer and number, or by its key identifier.
func signerCert(certs []*x509.Certificate, sid asn1.RawValue) *x509.Certificate {
	if sid.Class == asn1.ClassContextSpecific && sid.Tag == 0 { // subjectKeyIdentifier
		for _, c := range certs {
			if bytes.Equal(c.SubjectKeyId, sid.Bytes) {
				return c
			}
		}
		return nil
	}
	var is issuerSerial
	if _, err := asn1.Unmarshal(sid.FullBytes, &is); err != nil {
		return nil
	}
	for _, c := range certs {
		if c.SerialNumber.Cmp(is.Serial) == 0 && bytes.Equal(c.RawIssuer, is.Issuer.FullBytes) {
			return c
		}
	}
	return nil
}

// gostKey is the GOST public key of the certificate: its curve, the point and the key's algorithm.
func gostKey(spki []byte) (*curve, *point, string, error) {
	var info struct {
		Alg struct {
			Algorithm  asn1.ObjectIdentifier
			Parameters asn1.RawValue `asn1:"optional"`
		}
		Key asn1.BitString
	}
	if _, err := asn1.Unmarshal(spki, &info); err != nil {
		return nil, nil, "", ErrFormat
	}
	var params struct {
		Curve  asn1.ObjectIdentifier
		Digest asn1.ObjectIdentifier `asn1:"optional"`
	}
	if _, err := asn1.Unmarshal(info.Alg.Parameters.FullBytes, &params); err != nil {
		return nil, nil, "", ErrAlgorithm
	}
	c := curveOf(params.Curve.String())
	var raw []byte // LE(x) ‖ LE(y) in an OCTET STRING
	if _, err := asn1.Unmarshal(info.Key.Bytes, &raw); err != nil || c == nil || len(raw) != 2*c.size() {
		return nil, nil, "", ErrAlgorithm
	}
	n := c.size()
	return c, &point{le(raw[:n]), le(raw[n:])}, info.Alg.Algorithm.String(), nil
}

func certInfo(cert *x509.Certificate) Cert {
	c := Cert{Serial: strings.ToUpper(cert.SerialNumber.Text(16)), NotBefore: cert.NotBefore, NotAfter: cert.NotAfter}
	for _, n := range cert.Subject.Names {
		if set, ok := oidNames[n.Type.String()]; ok {
			if v, ok := n.Value.(string); ok {
				set(&c, strings.TrimSpace(v))
			}
		}
	}
	c.Issuer = cert.Issuer.CommonName
	if c.Issuer == "" && len(cert.Issuer.Organization) > 0 {
		c.Issuer = cert.Issuer.Organization[0]
	}
	var spki struct {
		Alg pkix.AlgorithmIdentifier
	}
	_, _ = asn1.Unmarshal(cert.RawSubjectPublicKeyInfo, &spki)
	switch a := spki.Alg.Algorithm.String(); {
	case a == oidGOST256:
		c.Alg = "GOST R 34.10-2012 256"
	case a == oidGOST512:
		c.Alg = "GOST R 34.10-2012 512"
	default:
		c.Alg = cert.PublicKeyAlgorithm.String()
	}
	return c
}

var errBER = errors.New("qes: bad BER")

// der turns BER's indefinite lengths into definite ones, as encoding/asn1 wants; DER passes as it is.
func der(b []byte) ([]byte, error) {
	out, rest, err := derOne(b)
	if err != nil || len(bytes.TrimRight(rest, "\x00")) > 0 {
		return nil, errBER
	}
	return out, nil
}

func derOne(b []byte) (out, rest []byte, err error) {
	if len(b) < 2 {
		return nil, nil, errBER
	}
	i := 1
	if b[0]&0x1f == 0x1f { // a long tag
		for i < len(b) && b[i]&0x80 != 0 {
			i++
		}
		i++
	}
	if i >= len(b) {
		return nil, nil, errBER
	}
	tag := b[:i]
	l := int(b[i])
	i++
	if l == 0x80 { // indefinite: the parts up to the end-of-contents 00 00
		if b[0]&0x20 == 0 {
			return nil, nil, errBER
		}
		var body []byte
		rest = b[i:]
		for {
			if len(rest) >= 2 && rest[0] == 0 && rest[1] == 0 {
				rest = rest[2:]
				break
			}
			var part []byte
			if part, rest, err = derOne(rest); err != nil {
				return nil, nil, err
			}
			body = append(body, part...)
		}
		return append(append(append([]byte{}, tag...), length(len(body))...), body...), rest, nil
	}
	if l&0x80 != 0 {
		n := l & 0x7f
		if n == 0 || n > 4 || i+n > len(b) {
			return nil, nil, errBER
		}
		l = 0
		for _, c := range b[i : i+n] {
			l = l<<8 | int(c)
		}
		i += n
	}
	if l < 0 || i+l > len(b) {
		return nil, nil, errBER
	}
	if b[0]&0x20 == 0 || !bytes.Contains(b[i:i+l], []byte{0x80}) { // primitive, or no indefinite length inside
		return b[:i+l], b[i+l:], nil
	}
	var body []byte
	for in := b[i : i+l]; len(in) > 0; {
		var part []byte
		if part, in, err = derOne(in); err != nil {
			return nil, nil, err
		}
		body = append(body, part...)
	}
	return append(append(append([]byte{}, tag...), length(len(body))...), body...), b[i+l:], nil
}

func length(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var l []byte
	for ; n > 0; n >>= 8 {
		l = append([]byte{byte(n)}, l...)
	}
	return append([]byte{0x80 | byte(len(l))}, l...)
}

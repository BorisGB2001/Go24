// Package qestest makes qualified signatures for tests, as the CryptoPro plug-in would: a detached CAdES-BES
// (CMS SignedData) with the signer's certificate.
package qestest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"math/big"
	"time"
)

var (
	oidData          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidSignedData    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidAttrType      = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	oidMessageDigest = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidSigningTime   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 5}
)

type issuerSerial struct {
	Issuer asn1.RawValue
	Serial *big.Int
}

// Subject is a Russian qualified certificate's subject: a director of an organization.
var Subject = pkix.Name{CommonName: "ООО «Ромашка»", Organization: []string{"ООО «Ромашка»"}, ExtraNames: []pkix.AttributeTypeAndValue{
	{Type: asn1.ObjectIdentifier{2, 5, 4, 4}, Value: "Иванов"},
	{Type: asn1.ObjectIdentifier{2, 5, 4, 42}, Value: "Иван Иванович"},
	{Type: asn1.ObjectIdentifier{2, 5, 4, 12}, Value: "Генеральный директор"},
	{Type: asn1.ObjectIdentifier{1, 2, 643, 100, 4}, Value: "7701234567"},
	{Type: asn1.ObjectIdentifier{1, 2, 643, 100, 1}, Value: "1027700000001"},
	{Type: asn1.ObjectIdentifier{1, 2, 643, 100, 3}, Value: "12345678901"},
	{Type: asn1.ObjectIdentifier{1, 2, 643, 3, 131, 1, 1}, Value: "770100000001"},
}}

type attr struct {
	Type  asn1.ObjectIdentifier
	Value asn1.RawValue
}

// setOf is a SET of the one value, as attributes keep their values.
func setOf(v any) asn1.RawValue {
	return asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagSet, IsCompound: true, Bytes: raw(v).FullBytes}
}

func raw(v any) asn1.RawValue {
	b, err := asn1.Marshal(v)
	if err != nil {
		panic(err)
	}
	return asn1.RawValue{FullBytes: b}
}

// CMS is a detached SignedData of one signer, as CryptoPro makes it: the signed attributes with the document's
// digest (at — the signing time), signed by sign.
func CMS(certDER []byte, digestOID, sigOID asn1.ObjectIdentifier, digest []byte, at time.Time, sign func([]byte) []byte) ([]byte, error) {
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, err
	}
	attrs := []attr{
		{oidAttrType, setOf(oidData)},
		{oidSigningTime, setOf(at.UTC())},
		{oidMessageDigest, setOf(digest)},
	}
	set, err := asn1.MarshalWithParams(attrs, "set")
	if err != nil {
		return nil, err
	}
	implicit := append([]byte{0xa0}, set[1:]...)
	si := struct {
		Version   int
		SID       issuerSerial
		DigestAlg pkix.AlgorithmIdentifier
		Attrs     asn1.RawValue
		SigAlg    pkix.AlgorithmIdentifier
		Sig       []byte
	}{1, issuerSerial{asn1.RawValue{FullBytes: cert.RawIssuer}, cert.SerialNumber}, pkix.AlgorithmIdentifier{Algorithm: digestOID},
		asn1.RawValue{FullBytes: implicit}, pkix.AlgorithmIdentifier{Algorithm: sigOID}, sign(set)}
	sd := struct {
		Version    int
		DigestAlgs []pkix.AlgorithmIdentifier `asn1:"set"`
		Encap      struct{ Type asn1.ObjectIdentifier }
		Certs      asn1.RawValue
		Signers    []any `asn1:"set"`
	}{1, []pkix.AlgorithmIdentifier{{Algorithm: digestOID}}, struct{ Type asn1.ObjectIdentifier }{oidData},
		asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: certDER}, []any{si}}
	return asn1.Marshal(struct {
		Type    asn1.ObjectIdentifier
		Content asn1.RawValue
	}{oidSignedData, asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: raw(sd).FullBytes}})
}

// ECDSA signs doc at the time at with a new self-signed ECDSA P-256 certificate of Subject valid from … to.
func ECDSA(doc []byte, from, to, at time.Time) ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: Subject, NotBefore: from, NotAfter: to}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, err
	}
	d := sha256.Sum256(doc)
	var failed error
	sig, err := CMS(certDER, asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}, asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 2}, d[:], at,
		func(b []byte) []byte {
			h := sha256.Sum256(b)
			s, err := key.Sign(rand.Reader, h[:], crypto.SHA256)
			failed = err
			return s
		})
	if failed != nil {
		return nil, failed
	}
	return sig, err
}

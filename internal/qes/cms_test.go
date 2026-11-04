package qes

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"errors"
	"math/big"
	"testing"
	"time"

	"go24/internal/qes/qestest"
)

var (
	oidSHA256     = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidStreebog25 = asn1.ObjectIdentifier{1, 2, 643, 7, 1, 1, 2, 2}
)

func buildCMS(t *testing.T, certDER []byte, digestOID, sigOID asn1.ObjectIdentifier, digest []byte, at time.Time, sign func([]byte) []byte) []byte {
	t.Helper()
	out, err := qestest.CMS(certDER, digestOID, sigOID, digest, at, sign)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

var subject = qestest.Subject

func TestVerifyRSAandECDSA(t *testing.T) {
	doc := []byte("%PDF-1.7 the contract")
	now := time.Now()
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(0x1234ABCD), Subject: subject, // self-signed: the issuer is the subject
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour)}
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	ek, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	for _, c := range []struct {
		key    crypto.Signer
		sigOID asn1.ObjectIdentifier
		alg    string
	}{
		{rk, asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}, "RSA"},
		{ek, asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 2}, "ECDSA"},
	} {
		certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, c.key.Public(), c.key)
		if err != nil {
			t.Fatal(err)
		}
		d := sha256.Sum256(doc)
		sig := buildCMS(t, certDER, oidSHA256, c.sigOID, d[:], now, func(b []byte) []byte {
			h := sha256.Sum256(b)
			s, err := c.key.Sign(rand.Reader, h[:], crypto.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			return s
		})
		got, err := Verify(Decode([]byte(base64.StdEncoding.EncodeToString(sig))), doc, now)
		if err != nil {
			t.Fatalf("%s: %v", c.alg, err)
		}
		if got.Owner != "ООО «Ромашка»" || got.Surname != "Иванов" || got.Given != "Иван Иванович" || got.INNLE != "7701234567" ||
			got.OGRN != "1027700000001" || got.SNILS != "12345678901" || got.INN != "770100000001" || got.Title != "Генеральный директор" ||
			got.Serial != "1234ABCD" || got.Issuer != "ООО «Ромашка»" || got.Alg != c.alg || got.SigningTime == nil {
			t.Fatalf("%s: %+v", c.alg, got)
		}
		if _, err := Verify(sig, []byte("another document"), now); !errors.Is(err, ErrDocument) {
			t.Fatalf("%s: another document: %v", c.alg, err)
		}
		bad := buildCMS(t, certDER, oidSHA256, c.sigOID, d[:], now, func(b []byte) []byte {
			h := sha256.Sum256(append(b, 1))
			s, _ := c.key.Sign(rand.Reader, h[:], crypto.SHA256)
			return s
		})
		if _, err := Verify(bad, doc, now); !errors.Is(err, ErrSignature) {
			t.Fatalf("%s: a bad signature: %v", c.alg, err)
		}
		late := buildCMS(t, certDER, oidSHA256, c.sigOID, d[:], now.Add(48*time.Hour), func(b []byte) []byte {
			h := sha256.Sum256(b)
			s, _ := c.key.Sign(rand.Reader, h[:], crypto.SHA256)
			return s
		})
		if _, err := Verify(late, doc, now); !errors.Is(err, ErrExpired) {
			t.Fatalf("%s: signed after the certificate's end: %v", c.alg, err)
		}
	}
	if _, err := Verify([]byte("not a signature"), doc, now); !errors.Is(err, ErrFormat) {
		t.Fatalf("garbage: %v", err)
	}
}

// gostCert is a certificate with a GOST R 34.10-2012 256 key on CryptoPro-A (its own signature is not checked).
func gostCert(t *testing.T, pub *point, from, to time.Time) []byte {
	t.Helper()
	key := make([]byte, 64)
	x, y := pub.x.FillBytes(make([]byte, 32)), pub.y.FillBytes(make([]byte, 32))
	for i := 0; i < 32; i++ {
		key[i], key[32+i] = x[31-i], y[31-i]
	}
	keyOct, _ := asn1.Marshal(key)
	params, _ := asn1.Marshal(struct{ Curve, Digest asn1.ObjectIdentifier }{asn1.ObjectIdentifier{1, 2, 643, 2, 2, 35, 1}, oidStreebog25})
	alg := pkix.AlgorithmIdentifier{Algorithm: asn1.ObjectIdentifier{1, 2, 643, 7, 1, 1, 1, 1}, Parameters: asn1.RawValue{FullBytes: params}}
	sigAlg := pkix.AlgorithmIdentifier{Algorithm: asn1.ObjectIdentifier{1, 2, 643, 7, 1, 1, 3, 2}}
	issuer, _ := asn1.Marshal(pkix.Name{CommonName: "Тестовый УЦ", Organization: []string{"УЦ"}}.ToRDNSequence())
	subj, _ := asn1.Marshal(subject.ToRDNSequence())
	tbs := struct {
		Version  int `asn1:"explicit,tag:0"`
		Serial   *big.Int
		SigAlg   pkix.AlgorithmIdentifier
		Issuer   asn1.RawValue
		Validity struct{ From, To time.Time }
		Subject  asn1.RawValue
		SPKI     struct {
			Alg pkix.AlgorithmIdentifier
			Key asn1.BitString
		}
	}{2, big.NewInt(0x7c0001), sigAlg, asn1.RawValue{FullBytes: issuer}, struct{ From, To time.Time }{from.UTC(), to.UTC()},
		asn1.RawValue{FullBytes: subj}, struct {
			Alg pkix.AlgorithmIdentifier
			Key asn1.BitString
		}{alg, asn1.BitString{Bytes: keyOct, BitLength: len(keyOct) * 8}}}
	der, err := asn1.Marshal(struct {
		TBS    any
		SigAlg pkix.AlgorithmIdentifier
		Sig    asn1.BitString
	}{tbs, sigAlg, asn1.BitString{Bytes: make([]byte, 64), BitLength: 512}})
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestVerifyGOST(t *testing.T) {
	c := curveOf("1.2.643.2.2.35.1")
	d, _ := rand.Int(rand.Reader, c.q)
	pub := c.mul(d, &point{c.x, c.y})
	now := time.Now()
	certDER := gostCert(t, pub, now.Add(-time.Hour), now.Add(365*24*time.Hour))
	doc := []byte("%PDF-1.7 the contract signed with КЭП")
	h := NewStreebog256()
	h.Write(doc)
	sign := func(b []byte) []byte {
		h := NewStreebog256()
		h.Write(b)
		return gostSign(c, d, le(h.Sum(nil)))
	}
	sig := buildCMS(t, certDER, oidStreebog25, asn1.ObjectIdentifier{1, 2, 643, 7, 1, 1, 1, 1}, h.Sum(nil), now, sign)
	got, err := Verify(sig, doc, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Alg != "GOST R 34.10-2012 256" || got.Issuer != "Тестовый УЦ" || got.Serial != "7C0001" || got.SNILS != "12345678901" {
		t.Fatalf("%+v", got)
	}
	if _, err := Verify(sig, append(doc, ' '), now); !errors.Is(err, ErrDocument) {
		t.Fatalf("another document: %v", err)
	}
	other, _ := rand.Int(rand.Reader, c.q) // signed by another key than the certificate's
	bad := buildCMS(t, certDER, oidStreebog25, asn1.ObjectIdentifier{1, 2, 643, 7, 1, 1, 3, 2}, h.Sum(nil), now, func(b []byte) []byte {
		h := NewStreebog256()
		h.Write(b)
		return gostSign(c, other, le(h.Sum(nil)))
	})
	if _, err := Verify(bad, doc, now); !errors.Is(err, ErrSignature) {
		t.Fatalf("another key: %v", err)
	}
}

// BER with indefinite lengths, as some tools write CMS, is read as DER.
func TestDER(t *testing.T) {
	inner, _ := asn1.Marshal([]int{1, 2})
	ber := append(append([]byte{0x30, 0x80}, inner...), 0, 0)
	got, err := der(ber)
	want, _ := asn1.Marshal(struct{ A []int }{[]int{1, 2}})
	if err != nil || string(got) != string(want) {
		t.Fatalf("der(%x) = %x, %v; want %x", ber, got, err, want)
	}
}

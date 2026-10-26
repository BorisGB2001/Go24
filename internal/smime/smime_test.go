package smime

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testCA makes a CA and a certificate of the address signed by it (RSA, or ECDSA).
func testCA(t *testing.T, email string, ec bool) (ca *x509.Certificate, caKey crypto.Signer, cert *x509.Certificate, key crypto.Signer) {
	t.Helper()
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	caKey = k
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "go24 test CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, k.Public(), k)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ = x509.ParseCertificate(der)
	if ec {
		key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	} else {
		key, _ = rsa.GenerateKey(rand.Reader, 2048)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	ut := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: email}, EmailAddresses: []string{email},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}}
	der, err = x509.CreateCertificate(rand.Reader, ut, ca, key.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ = x509.ParseCertificate(der)
	return
}

func TestSignVerify(t *testing.T) {
	for _, ec := range []bool{false, true} {
		ca, _, cert, key := testCA(t, "anna@go24.test", ec)
		entity := []byte("Content-Type: text/plain; charset=utf-8\r\n\r\nПривет\r\n")
		p7, err := Sign(entity, cert, []*x509.Certificate{ca}, key, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		s, err := Verify(p7, entity)
		if err != nil || s.Signer == nil || !s.Signer.Equal(cert) || len(s.Certs) != 2 || s.Time.IsZero() {
			t.Fatalf("ec %v: %+v %v", ec, s, err)
		}
		if err := s.Trust(nil); err == nil {
			t.Fatal("a test CA trusted by the system")
		}
		roots := x509.NewCertPool()
		roots.AddCert(ca)
		if err := s.Trust(roots); err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(p7, append(entity, 'x')); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("a changed letter: %v", err)
		}
		if !IsSigned(p7) || IsEnveloped(p7) {
			t.Fatal("kind")
		}
	}
}

func TestEncryptDecrypt(t *testing.T) {
	_, _, anna, annaKey := testCA(t, "anna@go24.test", false)
	_, _, bob, bobKey := testCA(t, "bob@go24.test", false)
	_, _, eve, eveKey := testCA(t, "eve@go24.test", false)
	entity := bytes.Repeat([]byte("Content-Type: text/plain\r\n\r\nсекрет\r\n"), 100)
	p7, err := Encrypt(entity, []*x509.Certificate{anna, bob})
	if err != nil {
		t.Fatal(err)
	}
	for i, who := range []struct {
		c *x509.Certificate
		k crypto.Signer
	}{{anna, annaKey}, {bob, bobKey}} {
		got, err := Decrypt(p7, who.c, who.k.(crypto.Decrypter))
		if err != nil || !bytes.Equal(got, entity) {
			t.Fatalf("%d: %v", i, err)
		}
	}
	if _, err := Decrypt(p7, eve, eveKey.(crypto.Decrypter)); !errors.Is(err, ErrNotForMe) {
		t.Fatalf("eve: %v", err)
	}
	_, _, ec, _ := testCA(t, "ec@go24.test", true)
	if _, err := Encrypt(entity, []*x509.Certificate{ec}); !errors.Is(err, ErrKey) {
		t.Fatalf("EC recipient: %v", err)
	}
	if !IsEnveloped(p7) || IsSigned(p7) {
		t.Fatal("kind")
	}
}

func TestBER(t *testing.T) {
	// an OCTET STRING in two pieces of indefinite length inside a SEQUENCE of indefinite length
	b := []byte{0x30, 0x80, 0x24, 0x80, 0x04, 0x02, 'a', 'b', 0x04, 0x01, 'c', 0, 0, 0x02, 0x01, 0x05, 0, 0}
	n, rest, err := parseBER(b)
	if err != nil || len(rest) != 0 || !n.seq() || string(n.kid(0).bytes()) != "abc" || intOf(n.kid(1)) != 5 {
		t.Fatalf("%+v %v", n, err)
	}
	if d := n.der(); !bytes.Equal(d, []byte{0x30, 0x08, 0x04, 0x03, 'a', 'b', 'c', 0x02, 0x01, 0x05}) {
		t.Fatalf("der % x", d)
	}
	for _, bad := range [][]byte{{0x30}, {0x30, 0x85, 1, 1, 1, 1, 1}, {0x30, 0x05, 0x04}, {0x04, 0x80, 0, 0}, bytes.Repeat([]byte{0x30, 0x80}, 100)} {
		if _, _, err := parseBER(bad); err == nil {
			t.Fatalf("% x read", bad)
		}
	}
}

func TestKDF(t *testing.T) {
	// a key of 24 bytes for 3DES from the password "smeg", salt 0A58CF64530D823F, 1 iteration (the vector of
	// OpenSSL's and BouncyCastle's tests of PKCS #12)
	got := kdf(sha1.New, 64, bmpString("smeg"), []byte{0x0A, 0x58, 0xCF, 0x64, 0x53, 0x0D, 0x82, 0x3F}, 1, 1, 24)
	want := []byte{0x8A, 0xAA, 0xE6, 0x29, 0x7B, 0x6C, 0xB0, 0x46, 0x42, 0xAB, 0x5B, 0x07, 0x78, 0x51, 0x28, 0x4E, 0xB7, 0x12, 0x8F, 0x1A, 0x2A, 0x7F, 0xBC, 0xA3}
	if !bytes.Equal(got, want) {
		t.Fatalf("% X", got)
	}
}

// TestOpenSSL: what OpenSSL writes go24 reads, and the other way — signatures, encryption (streamed BER too) and
// .pfx files (OpenSSL 3's AES, the old 3DES).
func TestOpenSSL(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("no openssl")
	}
	dir := t.TempDir()
	ca, _, cert, key := testCA(t, "anna@go24.test", false)
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	run := func(args ...string) []byte {
		t.Helper()
		out, err := exec.Command("openssl", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("openssl %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	kder, _ := x509.MarshalPKCS8PrivateKey(key)
	certPEM := write("cert.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
	keyPEM := write("key.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder}))
	caPEM := write("ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}))
	entity := []byte("Content-Type: text/plain; charset=utf-8\r\n\r\nПривет от go24\r\n")
	content := write("content", entity)

	// go24 signs and encrypts, OpenSSL checks and decrypts
	p7, _ := Sign(entity, cert, nil, key, time.Now())
	run("cms", "-verify", "-binary", "-inform", "DER", "-in", write("sig.der", p7), "-content", content, "-CAfile", caPEM, "-out", filepath.Join(dir, "v"))
	enc, _ := Encrypt(entity, []*x509.Certificate{cert})
	if out := run("cms", "-decrypt", "-binary", "-inform", "DER", "-in", write("enc.der", enc), "-recip", certPEM, "-inkey", keyPEM); !bytes.Equal(out, entity) {
		t.Fatalf("openssl decrypted %q", out)
	}

	// OpenSSL signs (detached and inside, streamed BER) and encrypts, go24 checks and decrypts
	for _, args := range [][]string{{"-sign", "-binary"}, {"-sign", "-binary", "-nodetach", "-stream", "-indef"}, {"-sign", "-binary", "-md", "sha1"}} {
		sig := run(append(append([]string{"cms"}, args...), "-in", content, "-signer", certPEM, "-inkey", keyPEM, "-outform", "DER")...)
		detached := entity
		if strings.Contains(strings.Join(args, " "), "nodetach") {
			detached = nil
		}
		s, err := Verify(sig, detached)
		if err != nil || !bytes.Equal(s.Content, entity) || !s.Signer.Equal(cert) {
			t.Fatalf("%v: %v", args, err)
		}
	}
	for _, c := range []string{"-aes256", "-aes128", "-des3"} {
		out := run("cms", "-encrypt", "-binary", c, "-stream", "-indef", "-in", content, "-outform", "DER", certPEM)
		got, err := Decrypt(out, cert, key.(crypto.Decrypter))
		if err != nil || !bytes.Equal(got, entity) {
			t.Fatalf("%s: %v %q", c, err, got)
		}
	}
	oaep := run("cms", "-encrypt", "-binary", "-aes256", "-in", content, "-outform", "DER", "-recip", certPEM, "-keyopt", "rsa_padding_mode:oaep", "-keyopt", "rsa_oaep_md:sha256")
	if got, err := Decrypt(oaep, cert, key.(crypto.Decrypter)); err != nil || !bytes.Equal(got, entity) {
		t.Fatalf("OAEP: %v", err)
	}

	// .pfx files
	for _, args := range [][]string{nil, {"-keypbe", "PBE-SHA1-3DES", "-certpbe", "PBE-SHA1-3DES", "-macalg", "sha1"}, {"-certpbe", "NONE"}} {
		pfx := run(append([]string{"pkcs12", "-export", "-in", certPEM, "-inkey", keyPEM, "-certfile", caPEM, "-passout", "pass:Пароль1"}, args...)...)
		id, err := ParsePFX(pfx, "Пароль1")
		if err != nil || !id.Cert.Equal(cert) || len(id.Chain) != 1 || !id.Chain[0].Equal(ca) || !samePublic(id.Key.Public(), cert.PublicKey) {
			t.Fatalf("pfx %v: %+v %v", args, id, err)
		}
		if _, err := ParsePFX(pfx, "wrong"); !errors.Is(err, ErrPassword) {
			t.Fatalf("pfx %v, a wrong password: %v", args, err)
		}
	}
	empty := run("pkcs12", "-export", "-in", certPEM, "-inkey", keyPEM, "-passout", "pass:")
	if _, err := ParsePFX(empty, ""); err != nil {
		t.Fatalf("no password: %v", err)
	}
	if _, err := ParsePFX(run("pkcs12", "-export", "-in", certPEM, "-nokeys", "-passout", "pass:x"), "x"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no key: %v", err)
	}
	both, _ := os.ReadFile(certPEM)
	kb, _ := os.ReadFile(keyPEM)
	if id, err := ParsePEM(append(kb, both...)); err != nil || !id.Cert.Equal(cert) {
		t.Fatalf("pem: %v", err)
	}
}

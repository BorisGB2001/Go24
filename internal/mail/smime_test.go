package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/smime"
)

// testCerts makes a CA and a certificate of each address, as PEM with its key (what AddCert takes).
func testCerts(t *testing.T, emails ...string) (map[string][]byte, map[string]smime.Identity) {
	t.Helper()
	caKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "go24 test CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, caKey.Public(), caKey)
	ca, _ := x509.ParseCertificate(der)
	pems, ids := map[string][]byte{}, map[string]smime.Identity{}
	for i, e := range emails {
		key, _ := rsa.GenerateKey(rand.Reader, 2048)
		ut := &x509.Certificate{SerialNumber: big.NewInt(int64(10 + i)), Subject: pkix.Name{CommonName: strings.Split(e, "@")[0]}, EmailAddresses: []string{e},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}}
		der, err := x509.CreateCertificate(rand.Reader, ut, ca, key.Public(), caKey)
		if err != nil {
			t.Fatal(err)
		}
		cert, _ := x509.ParseCertificate(der)
		k8, _ := x509.MarshalPKCS8PrivateKey(key)
		pems[e] = append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: k8})...)
		ids[e] = smime.Identity{Cert: cert, Chain: []*x509.Certificate{ca}, Key: key}
	}
	return pems, ids
}

// signedLetter is a letter signed as Outlook signs it: multipart/signed with the signature as smime.p7s.
func signedLetter(t *testing.T, id smime.Identity, from, text string) []byte {
	t.Helper()
	entity := []byte("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + text + "\r\n")
	p7, err := smime.Sign(entity, id.Cert, id.Chain, id.Key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return []byte("From: " + from + "\r\nTo: bob@go24.test\r\nSubject: s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/signed; protocol=\"application/x-pkcs7-signature\";\r\n" +
		" micalg=SHA1; boundary=\"b1\"\r\n\r\nThis is an S/MIME signed message\r\n\r\n--b1\r\n" + string(entity) + "\r\n--b1\r\n" +
		"Content-Type: application/x-pkcs7-signature; name=\"smime.p7s\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" + string(base64Lines(p7)) + "\r\n--b1--\r\n")
}

// TestUnsecure: a signed letter is checked and shown without its signature; changed — «изменено»; signed by
// someone else than the sender — so said; the opaque signed-data of old programs is opened too.
func TestUnsecure(t *testing.T) {
	_, ids := testCerts(t, "anna@go24.test")
	anna := ids["anna@go24.test"]
	s := &Store{}
	ctx := context.Background()
	raw, sec := s.unsecure(ctx, "u", signedLetter(t, anna, "anna@go24.test", "Привет"))
	sec.of("anna@go24.test")
	p := parseFull(bytes.NewReader(raw))
	if sec.sign != SignUntrusted || !strings.Contains(sec.signer, "anna@go24.test") || sec.cert == nil || p.body != "Привет" || len(p.files) != 0 {
		t.Fatalf("signed: %+v %q %d", sec, p.body, len(p.files))
	}
	if !bytes.HasPrefix(raw, []byte("From: anna@go24.test\r\nTo: bob@go24.test\r\nSubject: s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain")) {
		t.Fatalf("the letter as it reads: %q", raw)
	}
	_, sec = s.unsecure(ctx, "u", signedLetter(t, anna, "eve@go24.test", "Привет"))
	if sec.of("eve@go24.test"); sec.sign != SignOther {
		t.Fatalf("someone else's signature: %+v", sec)
	}
	changed := bytes.Replace(signedLetter(t, anna, "anna@go24.test", "Плати 100"), []byte("100"), []byte("900"), 1)
	if raw, sec = s.unsecure(ctx, "u", changed); sec.sign != SignBad || parseFull(bytes.NewReader(raw)).body != "Плати 900" {
		t.Fatalf("changed: %+v", sec)
	}
	plain := []byte("From: a@x.ru\r\nContent-Type: text/plain\r\n\r\nhi\r\n")
	if raw, sec = s.unsecure(ctx, "u", plain); !bytes.Equal(raw, plain) || sec != (secured{}) {
		t.Fatal("a plain letter changed")
	}
	// application/pkcs7-mime; smime-type=signed-data (needs openssl to make)
	entity := []byte("Content-Type: text/plain; charset=utf-8\r\n\r\nВнутри\r\n")
	opaque := signedInside(t, entity, anna)
	letter := "From: anna@go24.test\r\nSubject: o\r\nContent-Type: application/pkcs7-mime; smime-type=signed-data; name=smime.p7m\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		string(base64Lines(opaque))
	if raw, sec = s.unsecure(ctx, "u", []byte(letter)); sec.sign != SignUntrusted || parseFull(bytes.NewReader(raw)).body != "Внутри" {
		t.Fatalf("signed-data: %+v %q", sec, raw)
	}
}

// signedInside re-signs with the content inside (go24 writes detached signatures only; old programs write these).
func signedInside(t *testing.T, entity []byte, id smime.Identity) []byte {
	t.Helper()
	dir := t.TempDir()
	k8, _ := x509.MarshalPKCS8PrivateKey(id.Key)
	os.WriteFile(dir+"/c.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: id.Cert.Raw}), 0o600)
	os.WriteFile(dir+"/k.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: k8}), 0o600)
	os.WriteFile(dir+"/m", entity, 0o600)
	out, err := exec.Command("openssl", "cms", "-sign", "-binary", "-nodetach", "-in", dir+"/m", "-signer", dir+"/c.pem", "-inkey", dir+"/k.pem", "-outform", "DER").Output()
	if err != nil {
		t.Skip("no openssl: ", err)
	}
	return out
}

// TestSMIME: anna signs a letter to bob — bob sees the signature checked and learns her certificate; bob answers
// encrypted and signed — anna reads it decrypted; to someone whose certificate is unknown, or without a certificate
// of one's own, a letter is not sent; a letter encrypted to a certificate anna removed cannot be read.
func TestSMIME(t *testing.T) {
	url, host := os.Getenv("GO24_TEST_DATABASE_URL"), os.Getenv("GO24_TEST_MAIL_HOST")
	if url == "" || host == "" {
		t.Skip("GO24_TEST_DATABASE_URL or GO24_TEST_MAIL_HOST not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	s := NewStore(pool, "test-secret")
	n := time.Now().UnixNano()
	annaAddr, bobAddr := fmt.Sprintf("anna.%d@go24.test", n), fmt.Sprintf("bob.%d@go24.test", n)
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@u.test') RETURNING id`, name).Scan(&id))
		return id
	}
	anna, bob, eve := user("anna"), user("bob"), user("eve")
	box := func(owner, addr string) string {
		b, err := s.Add(ctx, owner, Settings{Email: addr, IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
		must(err)
		must(s.Sync(ctx, b))
		return b
	}
	annaBox, bobBox, eveBox := box(anna, annaAddr), box(bob, bobAddr), box(eve, fmt.Sprintf("eve.%d@go24.test", n))
	pems, _ := testCerts(t, annaAddr, bobAddr)

	if _, err := s.AddCert(ctx, anna, []byte("not a certificate"), ""); err == nil {
		t.Fatal("garbage taken as a certificate")
	}
	c, err := s.AddCert(ctx, anna, pems[annaAddr], "")
	must(err)
	if c.Email != annaAddr || len(c.SHA1) != 40 {
		t.Fatalf("cert: %+v", c)
	}
	if _, err := s.AddCert(ctx, anna, pems[annaAddr], ""); err != nil { // the same again: kept once
		t.Fatal(err)
	}
	_, err = s.AddCert(ctx, bob, pems[bobAddr], "")
	must(err)
	if list, _ := s.Certs(ctx, anna); len(list) != 1 || !s.HasCert(ctx, anna, strings.ToUpper(annaAddr)) || s.HasCert(ctx, eve, annaAddr) {
		t.Fatalf("certs: %+v", list)
	}

	if err := s.SendLetter(ctx, eve, eveBox, Letter{To: bobAddr, Subject: "x", Text: "x", Sign: true}); !isErr(err, "err.mail_smime_nocert") {
		t.Fatalf("signing without a certificate: %v", err)
	}
	must(s.SendLetter(ctx, anna, annaBox, Letter{To: bobAddr, Subject: "Подписано", Text: "Договор во вложении", Sign: true}))
	must(s.Sync(ctx, bobBox))
	got := func(boxID, subject string) (body, sign, signer, enc string) {
		t.Helper()
		must(pool.QueryRow(ctx, `SELECT x.body, x.smime_sign, x.smime_signer, x.smime_enc FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id
			WHERE x.mailbox_id = $1 AND f.role = 'inbox' AND x.subject = $2`, boxID, subject).Scan(&body, &sign, &signer, &enc))
		return
	}
	if body, sign, signer, enc := got(bobBox, "Подписано"); body != "Договор во вложении" || sign != SignUntrusted || !strings.Contains(signer, annaAddr) || enc != "" {
		t.Fatalf("bob got: %q %s %s %s", body, sign, signer, enc)
	}
	var sentSign string // anna's own copy
	must(pool.QueryRow(ctx, `SELECT x.smime_sign FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id WHERE x.mailbox_id = $1 AND f.role = 'sent'`, annaBox).Scan(&sentSign))
	if sentSign != SignUntrusted {
		t.Fatalf("anna's sent copy: %q", sentSign)
	}

	// bob knows anna's certificate now; eve does not
	if err := s.SendLetter(ctx, eve, eveBox, Letter{To: annaAddr, Subject: "x", Text: "x", Encrypt: true}); !isErr(err, "err.mail_smime_nocert") {
		t.Fatalf("encrypting without a certificate: %v", err)
	}
	if err := s.SendLetter(ctx, bob, bobBox, Letter{To: annaAddr + ", carol@x.ru", Subject: "x", Text: "x", Encrypt: true}); !isErr(err, "err.mail_smime_peers") {
		t.Fatalf("encrypting to an unknown recipient: %v", err)
	}
	must(s.SendLetter(ctx, bob, bobBox, Letter{To: annaAddr, Subject: "Секрет", Text: "Пароль от сейфа", HTML: "<p>Пароль <b>от сейфа</b></p>", Sign: true, Encrypt: true}))
	must(s.Sync(ctx, annaBox))
	if body, sign, signer, enc := got(annaBox, "Секрет"); body != "Пароль от сейфа" || sign != SignUntrusted || !strings.Contains(signer, bobAddr) || enc != EncYes {
		t.Fatalf("anna got: %q %s %s %s", body, sign, signer, enc)
	}
	// on the server it is encrypted: the text is not in the letter's source
	var id string
	must(pool.QueryRow(ctx, `SELECT x.id::text FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id WHERE x.mailbox_id = $1 AND f.role = 'inbox' AND x.subject = 'Секрет'`, annaBox).Scan(&id))
	src, err := s.Source(ctx, anna, id)
	must(err)
	if !bytes.Contains(src, []byte("application/pkcs7-mime")) || bytes.Contains(src, []byte("сейфа")) {
		t.Fatalf("the source: %.500s", src)
	}

	// anna forgets her certificate: what comes encrypted to it cannot be read
	must(s.DeleteCert(ctx, anna, c.ID))
	if err := s.DeleteCert(ctx, bob, c.ID); err == nil {
		t.Fatal("someone else's certificate deleted")
	}
	must(s.SendLetter(ctx, bob, bobBox, Letter{To: annaAddr, Subject: "Ещё секрет", Text: "x", Encrypt: true}))
	must(s.Sync(ctx, annaBox))
	if body, _, _, enc := got(annaBox, "Ещё секрет"); enc != EncNoKey || body != "" {
		t.Fatalf("no key: %q %s", body, enc)
	}

	// defaults
	must(s.SaveSecurity(ctx, anna, Security{Sign: true}))
	if x, err := s.Security(ctx, anna); err != nil || !x.Sign || x.Encrypt {
		t.Fatalf("security: %+v %v", x, err)
	}
}

func isErr(err error, key string) bool {
	e, ok := err.(*i18n.Error)
	return ok && e.Key == key
}

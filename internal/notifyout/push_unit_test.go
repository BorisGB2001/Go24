package notifyout

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// browser is the receiving side of a subscription, as a browser keeps it.
type browser struct {
	key  *ecdh.PrivateKey
	auth []byte
}

func newBrowser(t *testing.T, endpoint string) (browser, PushSub) {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	var sub PushSub
	sub.Endpoint, sub.Keys.P256dh, sub.Keys.Auth = endpoint, b64.EncodeToString(k.PublicKey().Bytes()), b64.EncodeToString(auth)
	return browser{k, auth}, sub
}

// decrypt reads a message as the browser does (RFC 8291 / RFC 8188).
func (br browser) decrypt(t *testing.T, msg []byte) []byte {
	t.Helper()
	salt, rs, idlen := msg[:16], binary.BigEndian.Uint32(msg[16:20]), int(msg[20])
	if rs != pushRecord || idlen != 65 {
		t.Fatalf("header rs %d idlen %d", rs, idlen)
	}
	asRaw := msg[21 : 21+idlen]
	as, err := ecdh.P256().NewPublicKey(asRaw)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := br.key.ECDH(as)
	if err != nil {
		t.Fatal(err)
	}
	info := append(append([]byte("WebPush: info\x00"), br.key.PublicKey().Bytes()...), asRaw...)
	ikm := hkdf(br.auth, shared, info, 32)
	cek := hkdf(salt, ikm, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := hkdf(salt, ikm, []byte("Content-Encoding: nonce\x00"), 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, msg[21+idlen:], nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if plain[len(plain)-1] != 2 {
		t.Fatalf("no last-record delimiter: %q", plain)
	}
	return plain[:len(plain)-1]
}

// TestHKDF: the RFC 5869 test case 1 (its first 32 bytes, 3cb25f25…c4c5bf).
func TestHKDF(t *testing.T) {
	ikm := make([]byte, 22)
	for i := range ikm {
		ikm[i] = 0x0b
	}
	salt := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	info := []byte{0xf0, 0xf1, 0xf2, 0xf3, 0xf4, 0xf5, 0xf6, 0xf7, 0xf8, 0xf9}
	if got := b64.EncodeToString(hkdf(salt, ikm, info, 32)); got != "PLJfJfqs1XqQQ09k0DYvKi0tCpDPGlpMXbAtVuzExb8" {
		t.Errorf("hkdf %s", got)
	}
}

// TestPushSend: the message reaches the push service encrypted for the browser and signed with the company's key;
// a subscription the service no longer knows is ErrGone; a bad one is refused.
func TestPushSend(t *testing.T) {
	p := &Push{TTL: time.Hour}
	token, err := p.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	bot := Bot{Channel: "push", Enabled: true, Server: "mailto:go24@example.com", token: token}
	pubName, err := p.Check(context.Background(), bot)
	if err != nil {
		t.Fatal(err)
	}
	key, pubRaw, _ := vapidKey(token)
	if pubName != b64.EncodeToString(pubRaw) || len(pubRaw) != 65 {
		t.Fatalf("public key %q", pubName)
	}

	var got *http.Request
	var body []byte
	status := http.StatusCreated
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, body = r, nil
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
	}))
	defer srv.Close()
	p.Client = srv.Client()

	br, sub := newBrowser(t, srv.URL+"/wpush/abc")
	if err := sub.Valid(); err != nil {
		t.Fatal(err)
	}
	chat, _ := json.Marshal(sub)
	if err := p.Send(context.Background(), bot, string(chat), Message{Text: "Новая задача «Тест»", Link: "https://go24.example/notifications/7"}); err != nil {
		t.Fatal(err)
	}
	if got.Header.Get("Content-Encoding") != "aes128gcm" || got.Header.Get("TTL") != "3600" || got.URL.Path != "/wpush/abc" {
		t.Errorf("headers %v path %s", got.Header, got.URL.Path)
	}
	var pl pushPayload
	if err := json.Unmarshal(br.decrypt(t, body), &pl); err != nil || pl.Body != "Новая задача «Тест»" || pl.URL != "https://go24.example/notifications/7" {
		t.Errorf("payload %+v %v", pl, err)
	}

	// the VAPID header: t — a JWT for the push service's origin signed with the key, k — the public key
	auth := got.Header.Get("Authorization")
	tok, k, ok := strings.Cut(strings.TrimPrefix(auth, "vapid t="), ", k=")
	if !ok || k != pubName {
		t.Fatalf("authorization %q", auth)
	}
	parts := strings.Split(tok, ".")
	claimsRaw, _ := b64.DecodeString(parts[1])
	var claims struct {
		Aud, Sub string
		Exp      int64
	}
	json.Unmarshal(claimsRaw, &claims)
	if claims.Aud != srv.URL || claims.Sub != "mailto:go24@example.com" || claims.Exp < time.Now().Add(11*time.Hour).Unix() {
		t.Errorf("claims %s", claimsRaw)
	}
	sig, _ := b64.DecodeString(parts[2])
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(&key.PublicKey, h[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Error("the JWT's signature does not verify")
	}

	status = http.StatusGone
	if err := p.Send(context.Background(), bot, string(chat), Message{Text: "x"}); !errors.Is(err, ErrGone) {
		t.Errorf("410: %v", err)
	}
	status = http.StatusTooManyRequests
	if err := p.Send(context.Background(), bot, string(chat), Message{Text: "x"}); err == nil || errors.Is(err, ErrGone) {
		t.Errorf("429: %v", err)
	}

	for _, bad := range []func(*PushSub){
		func(s *PushSub) { s.Endpoint = "http://push.example/x" },
		func(s *PushSub) { s.Endpoint = "https://user@push.example/x" },
		func(s *PushSub) { s.Keys.Auth = b64.EncodeToString([]byte("short")) },
		func(s *PushSub) { s.Keys.P256dh = b64.EncodeToString(make([]byte, 65)) },
	} {
		s := sub
		bad(&s)
		if s.Valid() == nil {
			t.Errorf("valid: %+v", s)
		}
	}
}

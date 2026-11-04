package notifyout

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Push: notifications on the phone (and the computer) without go24 open, as the portal mobile app's — the browser's
// own push service (Web Push) wakes go24's service worker, which shows the notification. The company's VAPID key is
// the channel's token, made by go24 itself when the administrators turn the channel on (Keyed); its public half is
// the «bot's name», which the browser subscribes with, and the bot's server is the company's contact for the push
// services (go24's https address, or mailto:). An employee turns push on on each of their devices (Devices):
// the chat of a device is its subscription (PushSub as JSON), each message is encrypted for it (RFC 8291) and signed
// with the company's key (RFC 8292).
type Push struct {
	Client *http.Client
	TTL    time.Duration // how long the push service keeps a message for a device that is off
}

func init() { Register(&Push{Client: &http.Client{Timeout: 20 * time.Second}, TTL: 24 * time.Hour}) }

func (p *Push) Name() string               { return "push" }
func (p *Push) NeedsServer() bool          { return false }
func (p *Push) LinkURL(Bot, string) string { return "" }
func (p *Push) Devices()                   {}

// Updates: a push service sends nothing back.
func (p *Push) Updates(_ context.Context, _ Bot, next int64) ([]Update, int64, error) {
	return nil, next, nil
}

// Keyed is a channel whose token go24 makes itself (Push: the company's VAPID key) instead of the administrators
// pasting one: SaveBot asks for it when the channel has none.
type Keyed interface{ NewToken() (string, error) }

// Devices is a channel that an employee links device by device (Push), not by a code or an address: the link keeps
// the kinds, its devices are the chats.
type Devices interface{ Devices() }

// NewToken makes a VAPID key: a P-256 private key, PKCS #8 in base64url.
func (p *Push) NewToken() (string, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return "", err
	}
	return b64.EncodeToString(der), nil
}

var b64 = base64.RawURLEncoding

var errPushKey = errors.New("notifyout: not a VAPID key")

// vapidKey reads the token: the private key and its public half (65 bytes, uncompressed).
func vapidKey(token string) (*ecdsa.PrivateKey, []byte, error) {
	der, err := b64.DecodeString(token)
	if err != nil {
		return nil, nil, errPushKey
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, nil, errPushKey
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok || ek.Curve != elliptic.P256() {
		return nil, nil, errPushKey
	}
	pub, err := ek.PublicKey.ECDH()
	if err != nil {
		return nil, nil, errPushKey
	}
	return ek, pub.Bytes(), nil
}

// Check reads the key: the «bot's name» is its public half in base64url, the browser's applicationServerKey.
func (p *Push) Check(_ context.Context, b Bot) (string, error) {
	_, pub, err := vapidKey(b.Token())
	if err != nil {
		return "", err
	}
	return b64.EncodeToString(pub), nil
}

// PushSub is a browser's subscription, as PushSubscription.toJSON() gives it.
type PushSub struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

var errPushSub = errors.New("notifyout: not a push subscription")

// Valid: the endpoint is a push service's https address and the keys are a P-256 point and a 16-byte secret.
func (s PushSub) Valid() error {
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(s.Endpoint) > 2048 {
		return errPushSub
	}
	pub, err := b64.DecodeString(strings.TrimRight(s.Keys.P256dh, "="))
	if err != nil {
		return errPushSub
	}
	if _, err := ecdh.P256().NewPublicKey(pub); err != nil {
		return errPushSub
	}
	if auth, err := b64.DecodeString(strings.TrimRight(s.Keys.Auth, "=")); err != nil || len(auth) != 16 {
		return errPushSub
	}
	return nil
}

// pushPayload is what the service worker shows: the notification's line, where a tap leads, and its tag (a newer
// message of the same tag replaces the shown one).
type pushPayload struct {
	Body string `json:"body"`
	URL  string `json:"url,omitempty"`
	Tag  string `json:"tag,omitempty"`
}

// Send encrypts the notification for the device (chat: its PushSub as JSON) and hands it to its push service; the
// service no longer knowing the subscription (404, 410) is ErrGone.
func (p *Push) Send(ctx context.Context, b Bot, chat string, m Message) error {
	var sub PushSub
	if err := json.Unmarshal([]byte(chat), &sub); err != nil {
		return ErrGone
	}
	if sub.Valid() != nil {
		return ErrGone
	}
	key, pub, err := vapidKey(b.Token())
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(pushPayload{Body: m.Text, URL: m.Link, Tag: m.Link})
	body, err := pushEncrypt(sub, payload)
	if err != nil {
		return err
	}
	u, _ := url.Parse(sub.Endpoint)
	jwt, err := vapidJWT(key, u.Scheme+"://"+u.Host, b.Server, time.Now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", fmt.Sprint(int(p.TTL.Seconds())))
	req.Header.Set("Urgency", "high")
	req.Header.Set("Authorization", "vapid t="+jwt+", k="+b64.EncodeToString(pub))
	res, err := p.Client.Do(req)
	if err != nil {
		return plain(err)
	}
	defer res.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		return ErrGone
	case res.StatusCode/100 != 2:
		return fmt.Errorf("push service: %s %s", res.Status, bytes.TrimSpace(msg))
	}
	return nil
}

// vapidJWT signs the claim of the company to the push service at aud (RFC 8292): ES256, valid for 12 hours.
func vapidJWT(key *ecdsa.PrivateKey, aud, sub string, now time.Time) (string, error) {
	head := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{"aud": aud, "exp": now.Add(12 * time.Hour).Unix(), "sub": sub})
	unsigned := head + "." + b64.EncodeToString(claims)
	h := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, key, h[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return unsigned + "." + b64.EncodeToString(sig), nil
}

// pushRecord is the record size written in the message's header; one record holds a notification.
const pushRecord = 4096

// pushEncrypt is the message for the subscription (RFC 8291, aes128gcm of RFC 8188): a key pair of its own for each
// message, its secret shared with the browser's key and mixed with the subscription's auth secret.
func pushEncrypt(sub PushSub, plain []byte) ([]byte, error) {
	uaRaw, err := b64.DecodeString(strings.TrimRight(sub.Keys.P256dh, "="))
	if err != nil {
		return nil, errPushSub
	}
	ua, err := ecdh.P256().NewPublicKey(uaRaw)
	if err != nil {
		return nil, errPushSub
	}
	auth, err := b64.DecodeString(strings.TrimRight(sub.Keys.Auth, "="))
	if err != nil {
		return nil, errPushSub
	}
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shared, err := as.ECDH(ua)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	asPub := as.PublicKey().Bytes()
	info := append(append([]byte("WebPush: info\x00"), uaRaw...), asPub...)
	ikm := hkdf(auth, shared, info, 32)
	cek := hkdf(salt, ikm, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := hkdf(salt, ikm, []byte("Content-Encoding: nonce\x00"), 12)
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(plain)+1+gcm.Overhead() > pushRecord {
		return nil, errors.New("notifyout: the push message is too long")
	}
	head := make([]byte, 0, 16+4+1+len(asPub))
	head = append(head, salt...)
	head = binary.BigEndian.AppendUint32(head, pushRecord)
	head = append(head, byte(len(asPub)))
	head = append(head, asPub...)
	// the only (last) record ends with the delimiter 2
	return gcm.Seal(head, nonce, append(plain[:len(plain):len(plain)], 2), nil), nil
}

// hkdf is HKDF-SHA-256 (RFC 5869) for up to one block of output.
func hkdf(salt, ikm, info []byte, n int) []byte {
	ext := hmac.New(sha256.New, salt)
	ext.Write(ikm)
	exp := hmac.New(sha256.New, ext.Sum(nil))
	exp.Write(info)
	exp.Write([]byte{1})
	return exp.Sum(nil)[:n]
}

package smime

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"hash"
	"math/big"
	"unicode/utf16"

	"golang.org/x/crypto/pbkdf2"
	"golang.org/x/crypto/pkcs12"
)

// The user's certificate and key, from a .pfx/.p12 file (what Windows' «Экспорт сертификата» and Outlook's
// «Импорт/экспорт» write; RFC 7292) or from PEM. New files (Windows 10+, OpenSSL 3) are encrypted with PBES2 and AES,
// older ones with 3DES (both read here) or RC2 (left to x/crypto/pkcs12).

var (
	// ErrPassword: the file's password is wrong.
	ErrPassword = errors.New("smime: wrong password")
	// ErrNoKey: the file holds no private key or no certificate of it.
	ErrNoKey = errors.New("smime: no certificate with its private key")
)

var (
	oidEncryptedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 6}
	oidKeyBag        = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 10, 1, 1}
	oidShroudedBag   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 10, 1, 2}
	oidCertBag       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 10, 1, 3}
	oidX509Cert      = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 22, 1}
	oidPBE3DES       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 1, 3}
	oidPBES2         = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 13}
	oidPBKDF2        = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 12}
	oidHMACSHA1      = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 7}
	oidHMACSHA256    = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 9}
	oidHMACSHA384    = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 10}
	oidHMACSHA512    = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 11}
)

// errLegacy: a cipher of old files (RC2) — the whole file goes to x/crypto/pkcs12.
var errLegacy = errors.New("smime: legacy PKCS #12 cipher")

// Identity is the user's certificate with its key and the rest of its chain.
type Identity struct {
	Cert  *x509.Certificate
	Chain []*x509.Certificate
	Key   crypto.Signer
}

// ParsePFX reads a .pfx/.p12 file with its password.
func ParsePFX(data []byte, password string) (Identity, error) {
	keys, certs, err := readPFX(data, password)
	if errors.Is(err, errLegacy) {
		blocks, e := pkcs12.ToPEM(data, password)
		if e != nil {
			if errors.Is(e, pkcs12.ErrIncorrectPassword) {
				return Identity{}, ErrPassword
			}
			return Identity{}, e
		}
		var b bytes.Buffer
		for _, bl := range blocks {
			pem.Encode(&b, &pem.Block{Type: bl.Type, Bytes: bl.Bytes})
		}
		return ParsePEM(b.Bytes())
	}
	if err != nil {
		return Identity{}, err
	}
	return identity(keys, certs)
}

// ParsePEM reads a certificate (with its chain) and its unencrypted private key from PEM.
func ParsePEM(data []byte) (Identity, error) {
	var keys []crypto.Signer
	var certs []*x509.Certificate
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			break
		}
		switch b.Type {
		case "CERTIFICATE":
			if c, err := x509.ParseCertificate(b.Bytes); err == nil {
				certs = append(certs, c)
			}
		case "PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY":
			if k := parseKey(b.Bytes); k != nil {
				keys = append(keys, k)
			}
		}
	}
	return identity(keys, certs)
}

func parseKey(der []byte) crypto.Signer {
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		if s, ok := k.(crypto.Signer); ok {
			return s
		}
	}
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k
	}
	if k, err := x509.ParseECPrivateKey(der); err == nil {
		return k
	}
	return nil
}

// identity pairs a key with its certificate (the same public key); the other certificates are its chain.
func identity(keys []crypto.Signer, certs []*x509.Certificate) (Identity, error) {
	for _, k := range keys {
		for _, c := range certs {
			if samePublic(k.Public(), c.PublicKey) {
				id := Identity{Cert: c, Key: k}
				for _, o := range certs {
					if o != c {
						id.Chain = append(id.Chain, o)
					}
				}
				return id, nil
			}
		}
	}
	return Identity{}, ErrNoKey
}

func samePublic(a, b crypto.PublicKey) bool {
	switch x := a.(type) {
	case *rsa.PublicKey:
		return x.Equal(b)
	case *ecdsa.PublicKey:
		return x.Equal(b)
	}
	return false
}

// readPFX: the keys and certificates of a PFX, its MAC checked with the password.
func readPFX(data []byte, password string) ([]crypto.Signer, []*x509.Certificate, error) {
	pfx, _, err := parseBER(data)
	if err != nil || !pfx.seq() || len(pfx.kids) < 2 {
		return nil, nil, errBER
	}
	auth := pfx.kid(1)
	if !auth.kid(0).oid().Equal(oidData) || !auth.kid(1).is(2, 0) {
		return nil, nil, ErrUnsupported // signed with a public key: no one uses it
	}
	safe := auth.kid(1).kid(0).bytes()
	bmp := bmpString(password)
	if mac := pfx.kid(2); mac.seq() {
		if err := checkMAC(mac, safe, password); err != nil {
			return nil, nil, err
		}
	}
	contents, _, err := parseBER(safe)
	if err != nil || !contents.seq() {
		return nil, nil, errBER
	}
	var keys []crypto.Signer
	var certs []*x509.Certificate
	for _, ci := range contents.kids {
		var bags []byte
		switch id := ci.kid(0).oid(); {
		case id.Equal(oidData):
			bags = ci.kid(1).kid(0).bytes()
		case id.Equal(oidEncryptedData):
			eci := ci.kid(1).kid(0).kid(1) // EncryptedData: version, EncryptedContentInfo
			if bags, err = pbeDecrypt(eci.kid(1), eci.kid(2).bytes(), password, bmp); err != nil {
				return nil, nil, err
			}
		default:
			continue
		}
		list, _, err := parseBER(bags)
		if err != nil || !list.seq() {
			return nil, nil, errBER
		}
		for _, bag := range list.kids {
			value := bag.kid(1).kid(0)
			switch id := bag.kid(0).oid(); {
			case id.Equal(oidCertBag):
				if value.kid(0).oid().Equal(oidX509Cert) {
					if c, err := x509.ParseCertificate(value.kid(1).kid(0).bytes()); err == nil {
						certs = append(certs, c)
					}
				}
			case id.Equal(oidKeyBag):
				if k := parseKey(value.der()); k != nil {
					keys = append(keys, k)
				}
			case id.Equal(oidShroudedBag):
				der, err := pbeDecrypt(value.kid(0), value.kid(1).bytes(), password, bmp)
				if err != nil {
					return nil, nil, err
				}
				if k := parseKey(der); k != nil {
					keys = append(keys, k)
				}
			}
		}
	}
	return keys, certs, nil
}

// checkMAC checks the MacData: HMAC of the safe contents with a key derived from the password (RFC 7292 B; an
// empty password is written either way — with its two zero bytes or without).
func checkMAC(mac node, safe []byte, password string) error {
	newHash, v := kdfHash(mac.kid(0).kid(0).kid(0).oid())
	if newHash == nil {
		return ErrUnsupported
	}
	want, salt := mac.kid(0).kid(1).bytes(), mac.kid(1).bytes()
	iter := 1
	if n := mac.kid(2); n.is(0, asn1.TagInteger) {
		iter = intOf(n)
	}
	passwords := [][]byte{bmpString(password)}
	if password == "" {
		passwords = append(passwords, nil)
	}
	for _, p := range passwords {
		key := kdf(newHash, v, p, salt, 3, iter, newHash().Size())
		m := hmac.New(newHash, key)
		m.Write(safe)
		if hmac.Equal(m.Sum(nil), want) {
			return nil
		}
	}
	return ErrPassword
}

// pbeDecrypt decrypts a piece of the file by its algorithm: PBES2 (PBKDF2 and AES or 3DES) or the old
// pbeWithSHAAnd3-KeyTripleDES-CBC.
func pbeDecrypt(algo node, data []byte, password string, bmp []byte) ([]byte, error) {
	var block cipher.Block
	var iv []byte
	switch id := algo.kid(0).oid(); {
	case id.Equal(oidPBE3DES):
		salt, iter := algo.kid(1).kid(0).bytes(), intOf(algo.kid(1).kid(1))
		key := kdf(sha1.New, 64, bmp, salt, 1, iter, 24)
		iv = kdf(sha1.New, 64, bmp, salt, 2, iter, 8)
		block, _ = des.NewTripleDESCipher(key)
	case id.Equal(oidPBES2):
		kd, enc := algo.kid(1).kid(0), algo.kid(1).kid(1)
		if !kd.kid(0).oid().Equal(oidPBKDF2) {
			return nil, ErrUnsupported
		}
		p := kd.kid(1)
		salt, iter := p.kid(0).bytes(), intOf(p.kid(1))
		prf := sha1.New
		for _, k := range p.kids[2:] {
			if k.seq() {
				switch id := k.kid(0).oid(); {
				case id.Equal(oidHMACSHA1):
				case id.Equal(oidHMACSHA256):
					prf = sha256.New
				case id.Equal(oidHMACSHA384):
					prf = sha512.New384
				case id.Equal(oidHMACSHA512):
					prf = sha512.New
				default:
					return nil, ErrUnsupported
				}
			}
		}
		iv = enc.kid(1).bytes()
		var size int
		switch id := enc.kid(0).oid(); {
		case id.Equal(oidAES128):
			size = 16
		case id.Equal(oidAES192):
			size = 24
		case id.Equal(oidAES256):
			size = 32
		case id.Equal(oid3DES):
			size = 24
		default:
			return nil, ErrUnsupported
		}
		key := pbkdf2.Key([]byte(password), salt, iter, size, prf)
		if size == 24 && enc.kid(0).oid().Equal(oid3DES) {
			block, _ = des.NewTripleDESCipher(key)
		} else {
			block, _ = aes.NewCipher(key)
		}
	default:
		return nil, errLegacy
	}
	if block == nil || len(iv) != block.BlockSize() || len(data) == 0 || len(data)%block.BlockSize() != 0 {
		return nil, errBER
	}
	out := append([]byte(nil), data...)
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, out)
	p := int(out[len(out)-1])
	if p == 0 || p > block.BlockSize() || !bytes.Equal(out[len(out)-p:], bytes.Repeat([]byte{byte(p)}, p)) {
		return nil, ErrPassword // the MAC is optional: a wrong password shows as bad padding
	}
	return out[:len(out)-p], nil
}

// kdfHash: the hash of a MAC and its block size for the KDF.
func kdfHash(id asn1.ObjectIdentifier) (func() hash.Hash, int) {
	switch {
	case id.Equal(oidSHA1):
		return sha1.New, 64
	case id.Equal(oidSHA256):
		return sha256.New, 64
	case id.Equal(oidSHA384):
		return sha512.New384, 128
	case id.Equal(oidSHA512):
		return sha512.New, 128
	}
	return nil, 0
}

// kdf is the key derivation of PKCS #12 (RFC 7292 appendix B): id 1 — a key, 2 — an IV, 3 — a MAC key.
func kdf(newHash func() hash.Hash, v int, password, salt []byte, id byte, iter, n int) []byte {
	fill := func(b []byte) []byte {
		if len(b) == 0 {
			return nil
		}
		out := make([]byte, v*((len(b)+v-1)/v))
		for i := range out {
			out[i] = b[i%len(b)]
		}
		return out
	}
	d := bytes.Repeat([]byte{id}, v)
	in := append(fill(salt), fill(password)...)
	var out []byte
	one := big.NewInt(1)
	for len(out) < n {
		h := newHash()
		h.Write(d)
		h.Write(in)
		a := h.Sum(nil)
		for r := 1; r < iter; r++ {
			h := newHash()
			h.Write(a)
			a = h.Sum(nil)
		}
		out = append(out, a...)
		if len(out) >= n {
			break
		}
		b := new(big.Int).SetBytes(fill(a)[:v])
		b.Add(b, one)
		for j := 0; j < len(in); j += v { // I_j = (I_j + B + 1) mod 2^(8v)
			x := new(big.Int).SetBytes(in[j : j+v])
			x.Add(x, b)
			xb := x.Bytes()
			if len(xb) > v {
				xb = xb[len(xb)-v:]
			}
			blk := in[j : j+v]
			for k := range blk {
				blk[k] = 0
			}
			copy(blk[v-len(xb):], xb)
		}
	}
	return out[:n]
}

// bmpString is the password as PKCS #12 takes it: UTF-16 big endian with two zero bytes at the end.
func bmpString(s string) []byte {
	var b []byte
	for _, r := range utf16.Encode([]rune(s)) {
		b = append(b, byte(r>>8), byte(r))
	}
	return append(b, 0, 0)
}

// maxIter bounds the iterations of a key derivation (files have 2000–600000; a hostile one could ask billions).
const maxIter = 1 << 21

// intOf is a count of the file: iterations, a version (1 when it is not one).
func intOf(n node) int {
	var i int
	if _, err := asn1.Unmarshal(n.full, &i); err != nil || i < 1 || i > maxIter {
		return 1
	}
	return i
}

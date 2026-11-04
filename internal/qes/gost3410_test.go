package qes

import (
	"crypto/rand"
	"encoding/hex"
	"math/big"
	"testing"
)

func hexInt(s string) *big.Int { v, _ := new(big.Int).SetString(s, 16); return v }

func hexBytes(s string) []byte { b, _ := hex.DecodeString(s); return b }

// gostSign signs the digest e with the key d, as the signer's tool would: s‖r, big-endian.
func gostSign(c *curve, d, e *big.Int) []byte {
	e = new(big.Int).Mod(e, c.q)
	if e.Sign() == 0 {
		e.SetInt64(1)
	}
	for {
		k, _ := rand.Int(rand.Reader, c.q)
		if k.Sign() == 0 {
			continue
		}
		r := new(big.Int).Mod(c.mul(k, &point{c.x, c.y}).x, c.q)
		s := new(big.Int).Mul(d, r)
		s.Add(s, k.Mul(k, e)).Mod(s, c.q)
		if r.Sign() == 0 || s.Sign() == 0 {
			continue
		}
		n := c.size()
		return append(s.FillBytes(make([]byte, n)), r.FillBytes(make([]byte, n))...)
	}
}

// The examples of GOST R 34.10-2012: appendix A.1 (256 bits, the test curve, the key and the signature) and A.2
// (512 bits, the public key and the signature).
func TestGOSTVerify(t *testing.T) {
	c := &curve{hexInt("8000000000000000000000000000000000000000000000000000000000000431"),
		hexInt("8000000000000000000000000000000150fe8a1892976154c59cfc193accf5b3"), big.NewInt(7),
		hexInt("5fbff498aa938ce739b8e022fbafef40563f6e6a3472fc2a514c0ce9dae23b7e"), big.NewInt(2),
		hexInt("08e2a8a0e65147d4bd6316030e16d19c85c97f0a9ca267122b96abbcea7e8fc8")}
	d := hexInt("7a929ade789bb9be10ed359dd39a72c11b60961f49397eee1d19ce9891ec3b28")
	pub := c.mul(d, &point{c.x, c.y})
	e := hexInt("2dfbc1b372d89a1188c09c52e0eec61fce52032ab1022e8e67ece6672b043ee5")
	sig := hexBytes("01456c64ba4642a1653c235a98a60249bcd6d3f746b631df928014f6c5bf9c40" + "41aa28d2f1ab148280cd9ed56feda41974053554a42767b83ad043fd39dc0493")
	if !c.verify(pub, e, sig) {
		t.Fatal("A.1: the signature of the standard is not good")
	}
	if c.verify(pub, new(big.Int).Add(e, big.NewInt(1)), sig) {
		t.Fatal("A.1: good for another digest")
	}
	if !c.verify(pub, e, gostSign(c, d, e)) {
		t.Fatal("A.1: our own signature is not good")
	}

	c = &curve{hexInt("4531acd1fe0023c7550d267b6b2fee80922b14b2ffb90f04d4eb7c09b5d2d15df1d852741af4704a0458047e80e4546d35b8336fac224dd81664bbf528be6373"),
		hexInt("4531acd1fe0023c7550d267b6b2fee80922b14b2ffb90f04d4eb7c09b5d2d15da82f2d7ecb1dbac719905c5eecc423f1d86e25edbe23c595d644aaf187e6e6df"),
		big.NewInt(7),
		hexInt("1cff0806a31116da29d8cfa54e57eb748bc5f377e49400fdd788b649eca1ac4361834013b2ad7322480a89ca58e0cf74bc9e540c2add6897fad0a3084f302adc"),
		hexInt("24d19cc64572ee30f396bf6ebbfd7a6c5213b3b3d7057cc825f91093a68cd762fd60611262cd838dc6b60aa7eee804e28bc849977fac33b4b530f1b120248a9a"),
		hexInt("2bb312a43bd2ce6e0d020613c857acddcfbf061e91e5f2c3f32447c259f39b2c83ab156d77f1496bf7eb3351e1ee4e43dc1a18b91b24640b6dbb92cb1add371e")}
	pub = &point{le(hexBytes("e1ef30d52c6133ddd99d1d5c41455cf7df4d8b4c925bbc69af1433d15658515add2146850c325c5b81c133be655aa8c4d440e7b98a8d59487b0c7696bcc55d11")),
		le(hexBytes("ecbe7736a9ec357ff2fd39931f4e114cb8cda359270ac7f0e7ff43d9419419ea61fd2ab77f5d9f63523d3b50a04f63e2a0cf51b7c13adc21560f0bd40cc9c737"))}
	if d := le(hexBytes("d48da11f826729c6dfaa18fd7b6b63a214277e82d2da223356a000223b12e87220108b508e50e70e70694651e8a09130c9d75677d43609a41b24aead8a04a60b")); c.mul(d, &point{c.x, c.y}).x.Cmp(pub.x) != 0 {
		t.Fatal("A.2: the public key is not of the private one")
	}
	e = hexInt("3754f3cfacc9e0615c4f4a7c4d8dab531b09b6f9c170c533a71d147035b0c5917184ee536593f4414339976c647c5d5a407adedb1d560c4fc6777d2972075b8c")
	sig = hexBytes("1081b394696ffe8e6585e7a9362d26b6325f56778aadbc081c0bfbe933d52ff5823ce288e8c4f362526080df7f70ce406a6eeb1f56919cb92a9853bde73e5b4a2f86fa60a081091a23dd795e1e3c689ee512a3c82ee0dcc2643c78eea8fcacd35492558486b20f1c9ec197c90699850260c93bcbcd9c5c3317e19344e173ae36")
	if !c.verify(pub, e, sig) {
		t.Fatal("A.2: the signature of the standard is not good")
	}
	sig[10] ^= 1
	if c.verify(pub, e, sig) {
		t.Fatal("A.2: a damaged signature is good")
	}
}

// Every curve of the table: its base point is on it and of order q.
func TestCurves(t *testing.T) {
	for oid := range curveParams {
		c := curveOf(oid)
		g := &point{c.x, c.y}
		if !c.on(g) || c.mul(c.q, g) != nil {
			t.Errorf("%s: the base point is wrong", oid)
		}
	}
}

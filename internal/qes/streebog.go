package qes

import (
	"encoding/binary"
	"encoding/hex"
	"hash"
)

// Streebog, GOST R 34.11-2012: the hash of Russian qualified signatures. The block is 64 bytes, kept as 8 words
// little-endian (as the standard numbers the bytes); the digest's bytes are in the order CMS keeps them.

var (
	lps    [8][256]uint64 // the table of the LPS transform: the substitution, transposition and linear transform at once
	rounds [12][8]uint64  // the round constants C as words
)

func init() {
	pi, _ := hex.DecodeString(streebogPi)
	ab, _ := hex.DecodeString(streebogA)
	var a [64]uint64
	for i := range a {
		a[i] = binary.BigEndian.Uint64(ab[i*8:])
	}
	for j := 0; j < 8; j++ { // byte j of a row: its bits, the highest first, pick the rows of A from (7-j)*8
		for v := 0; v < 256; v++ {
			var w uint64
			b := pi[v]
			for bit := 0; bit < 8; bit++ {
				if b&(0x80>>bit) != 0 {
					w ^= a[(7-j)*8+bit]
				}
			}
			lps[j][v] = w
		}
	}
	for i, c := range streebogC {
		cb, _ := hex.DecodeString(c)
		for k := 0; k < 8; k++ {
			rounds[i][k] = binary.LittleEndian.Uint64(cb[k*8:])
		}
	}
}

type block [8]uint64

// transform is LPS: after the transposition, row i is byte i of every word.
func transform(x block) (y block) {
	for i := 0; i < 8; i++ {
		s := uint(8 * i)
		y[i] = lps[0][byte(x[0]>>s)] ^ lps[1][byte(x[1]>>s)] ^ lps[2][byte(x[2]>>s)] ^ lps[3][byte(x[3]>>s)] ^
			lps[4][byte(x[4]>>s)] ^ lps[5][byte(x[5]>>s)] ^ lps[6][byte(x[6]>>s)] ^ lps[7][byte(x[7]>>s)]
	}
	return y
}

func xor(x, y block) (z block) {
	for i := range z {
		z[i] = x[i] ^ y[i]
	}
	return z
}

// compress is g_N(h, m) = E(LPS(h ⊕ N), m) ⊕ h ⊕ m.
func compress(n uint64, h, m block) block {
	k := h
	k[0] ^= n
	k = transform(k)
	e := m
	for i := 0; i < 12; i++ {
		e = transform(xor(k, e))
		k = transform(xor(k, rounds[i]))
	}
	return xor(xor(xor(k, e), h), m)
}

// add is a 512-bit sum modulo 2^512.
func add(x, y block) (z block) {
	var carry uint64
	for i := range z {
		s := x[i] + y[i]
		c := 0
		if s < x[i] {
			c = 1
		}
		s += carry
		if s < carry {
			c = 1
		}
		z[i], carry = s, uint64(c)
	}
	return z
}

func load(b []byte) (m block) {
	for i := range m {
		m[i] = binary.LittleEndian.Uint64(b[i*8:])
	}
	return m
}

type streebog struct {
	size     int
	h, sigma block
	n        uint64 // the bits hashed, mod 2^64 (enough for any document)
	buf      []byte
}

// NewStreebog256 and NewStreebog512 are Streebog with a 32- and 64-byte digest.
func NewStreebog256() hash.Hash { d := &streebog{size: 32}; d.Reset(); return d }
func NewStreebog512() hash.Hash { d := &streebog{size: 64}; d.Reset(); return d }

func (d *streebog) Size() int      { return d.size }
func (d *streebog) BlockSize() int { return 64 }

func (d *streebog) Reset() {
	d.h, d.sigma, d.n, d.buf = block{}, block{}, 0, d.buf[:0]
	if d.size == 32 {
		for i := range d.h {
			d.h[i] = 0x0101010101010101
		}
	}
}

func (d *streebog) Write(p []byte) (int, error) {
	d.buf = append(d.buf, p...)
	for len(d.buf) >= 64 {
		m := load(d.buf)
		d.h = compress(d.n, d.h, m)
		d.n += 512
		d.sigma = add(d.sigma, m)
		d.buf = d.buf[64:]
	}
	return len(p), nil
}

func (d *streebog) Sum(in []byte) []byte {
	var pad [64]byte
	copy(pad[:], d.buf)
	pad[len(d.buf)] = 1
	m := load(pad[:])
	h := compress(d.n, d.h, m)
	h = compress(0, h, block{d.n + uint64(len(d.buf))*8})
	h = compress(0, h, add(d.sigma, m))
	var out [64]byte
	for i, w := range h {
		binary.LittleEndian.PutUint64(out[i*8:], w)
	}
	return append(in, out[64-d.size:]...)
}

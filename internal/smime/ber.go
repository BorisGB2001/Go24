package smime

import (
	"bytes"
	"encoding/asn1"
	"errors"
)

// Reading BER. Mail programs send CMS in BER, not DER: Outlook and Thunderbird write indefinite lengths and break
// long OCTET STRINGs into pieces, which encoding/asn1 does not read. So go24 reads them as a tree of TLVs and writes
// DER (encoding/asn1) itself.

var errBER = errors.New("smime: bad BER")

// node is one TLV of BER.
type node struct {
	class, tag  int
	constructed bool
	body        []byte // the contents (primitive), or the encoding of the kids (constructed)
	kids        []node
	full        []byte // the whole TLV as it came
}

// maxDepth bounds the nesting of a TLV (a hostile letter).
const maxDepth = 64

// parseBER reads one TLV and what follows it.
func parseBER(b []byte) (node, []byte, error) {
	return parseAt(b, 0)
}

func parseAt(b []byte, depth int) (node, []byte, error) {
	var n node
	if depth > maxDepth || len(b) < 2 {
		return n, nil, errBER
	}
	start := b
	n.class, n.constructed, n.tag = int(b[0]>>6), b[0]&0x20 != 0, int(b[0]&0x1f)
	b = b[1:]
	if n.tag == 0x1f { // a long tag
		n.tag = 0
		for {
			if len(b) == 0 || n.tag > 1<<24 {
				return n, nil, errBER
			}
			c := b[0]
			b = b[1:]
			n.tag = n.tag<<7 | int(c&0x7f)
			if c&0x80 == 0 {
				break
			}
		}
	}
	if len(b) == 0 {
		return n, nil, errBER
	}
	l := int(b[0])
	b = b[1:]
	if l == 0x80 { // indefinite: the kids until 00 00
		if !n.constructed {
			return n, nil, errBER
		}
		for {
			if len(b) >= 2 && b[0] == 0 && b[1] == 0 {
				b = b[2:]
				break
			}
			kid, rest, err := parseAt(b, depth+1)
			if err != nil {
				return n, nil, err
			}
			n.kids = append(n.kids, kid)
			b = rest
		}
		n.full = start[:len(start)-len(b)]
		return n, b, nil
	}
	if l > 0x80 {
		k := l & 0x7f
		if k > 4 || len(b) < k {
			return n, nil, errBER
		}
		l = 0
		for _, c := range b[:k] {
			l = l<<8 | int(c)
		}
		b = b[k:]
	}
	if l < 0 || l > len(b) {
		return n, nil, errBER
	}
	n.body, b = b[:l], b[l:]
	n.full = start[:len(start)-len(b)]
	if n.constructed {
		for rest := n.body; len(rest) > 0; {
			kid, r, err := parseAt(rest, depth+1)
			if err != nil {
				return n, nil, err
			}
			n.kids = append(n.kids, kid)
			rest = r
		}
	}
	return n, b, nil
}

// is: a universal tag (asn1.TagSequence…), or a context one ([0] — class 2).
func (n node) is(class, tag int) bool { return n.class == class && n.tag == tag }

func (n node) seq() bool { return n.is(0, asn1.TagSequence) && n.constructed }

// bytes are the contents of a string type: a primitive one as is, a constructed one — its pieces joined.
func (n node) bytes() []byte {
	if !n.constructed {
		return n.body
	}
	var b bytes.Buffer
	for _, k := range n.kids {
		b.Write(k.bytes())
	}
	return b.Bytes()
}

// oid of an OBJECT IDENTIFIER.
func (n node) oid() asn1.ObjectIdentifier {
	if !n.is(0, asn1.TagOID) {
		return nil
	}
	var id asn1.ObjectIdentifier
	if _, err := asn1.Unmarshal(n.full, &id); err != nil {
		return nil
	}
	return id
}

// kid i, or a zero node.
func (n node) kid(i int) node {
	if i < 0 || i >= len(n.kids) {
		return node{class: -1}
	}
	return n.kids[i]
}

// der re-encodes the node in DER (definite lengths, string pieces joined) — what a certificate or a signed
// attribute is parsed and checked as.
func (n node) der() []byte {
	if n.constructed && n.class == 0 && (n.tag == asn1.TagOctetString || n.tag == asn1.TagBitString || n.tag == asn1.TagUTF8String ||
		n.tag == asn1.TagPrintableString || n.tag == asn1.TagIA5String) {
		return tlv(n.class, false, n.tag, n.bytes())
	}
	if !n.constructed || definite(n) {
		return n.full
	}
	var body []byte
	for _, k := range n.kids {
		body = append(body, k.der()...)
	}
	return tlv(n.class, true, n.tag, body)
}

// definite: written with definite lengths all down (then its bytes are DER enough to keep as they are).
func definite(n node) bool {
	if len(n.full) > 1 && n.full[1] == 0x80 {
		return false
	}
	for _, k := range n.kids {
		if k.constructed && !definite(k) {
			return false
		}
	}
	return true
}

// tlv writes a TLV in DER (tags below 31).
func tlv(class int, constructed bool, tag int, body []byte) []byte {
	b := byte(class<<6 | tag)
	if constructed {
		b |= 0x20
	}
	out := []byte{b}
	switch l := len(body); {
	case l < 0x80:
		out = append(out, byte(l))
	case l < 0x100:
		out = append(out, 0x81, byte(l))
	case l < 0x10000:
		out = append(out, 0x82, byte(l>>8), byte(l))
	case l < 0x1000000:
		out = append(out, 0x83, byte(l>>16), byte(l>>8), byte(l))
	default:
		out = append(out, 0x84, byte(l>>24), byte(l>>16), byte(l>>8), byte(l))
	}
	return append(out, body...)
}

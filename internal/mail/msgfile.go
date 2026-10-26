package mail

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf16"
)

// A letter of Outlook saved as a file (.msg) is a compound file (MS-CFB) of MAPI properties (MS-OXMSG): msgMIME
// turns it into a letter as a mail server keeps it, for «Импорт писем».

var errNotMsg = errors.New("not an Outlook letter")

// cfb is a compound file: its directory and the streams of its entries.
type cfb struct {
	data      []byte
	sector    int
	fat, mini []uint32
	ministrm  []byte
	dir       []cfbEntry
}

type cfbEntry struct {
	name               string
	kind               byte // 1 — a storage, 2 — a stream, 5 — the root
	left, right, child uint32
	start              uint32
	size               uint64
}

const (
	cfbEnd    = 0xFFFFFFFE // the end of a chain (and above: free, FAT and DIFAT sectors)
	cfbNoSibl = 0xFFFFFFFF
)

func readCFB(data []byte) (*cfb, error) {
	if len(data) < 512 || !bytes.Equal(data[:8], []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}) {
		return nil, errNotMsg
	}
	le := binary.LittleEndian
	shift := le.Uint16(data[0x1E:])
	if shift != 9 && shift != 12 {
		return nil, errNotMsg
	}
	f := &cfb{data: data, sector: 1 << shift}
	// the sectors of the FAT: 109 in the header, the rest in the DIFAT chain
	var fatSectors []uint32
	for i := 0; i < 109; i++ {
		if s := le.Uint32(data[0x4C+4*i:]); s < cfbEnd {
			fatSectors = append(fatSectors, s)
		}
	}
	for s, n := le.Uint32(data[0x44:]), le.Uint32(data[0x48:]); s < cfbEnd && n > 0; n-- {
		b := f.sectorAt(s)
		if b == nil {
			return nil, errNotMsg
		}
		for i := 0; i < len(b)/4-1; i++ {
			if x := le.Uint32(b[4*i:]); x < cfbEnd {
				fatSectors = append(fatSectors, x)
			}
		}
		s = le.Uint32(b[len(b)-4:])
	}
	for _, s := range fatSectors {
		b := f.sectorAt(s)
		if b == nil {
			return nil, errNotMsg
		}
		for i := 0; i < len(b)/4; i++ {
			f.fat = append(f.fat, le.Uint32(b[4*i:]))
		}
	}
	dir, err := f.chain(le.Uint32(data[0x30:]), -1)
	if err != nil {
		return nil, err
	}
	for i := 0; i+128 <= len(dir); i += 128 {
		e := dir[i : i+128]
		n := int(le.Uint16(e[64:]))
		if n > 64 {
			n = 64
		}
		u := make([]uint16, 0, n/2)
		for j := 0; j+1 < n; j += 2 {
			if c := le.Uint16(e[j:]); c != 0 {
				u = append(u, c)
			}
		}
		f.dir = append(f.dir, cfbEntry{name: string(utf16.Decode(u)), kind: e[66], left: le.Uint32(e[68:]), right: le.Uint32(e[72:]),
			child: le.Uint32(e[76:]), start: le.Uint32(e[116:]), size: le.Uint64(e[120:])})
	}
	if len(f.dir) == 0 || f.dir[0].kind != 5 {
		return nil, errNotMsg
	}
	if shift == 9 {
		f.dir[0].size &= 0xFFFFFFFF // version 3: the upper half may be garbage
	}
	mf, err := f.chain(le.Uint32(data[0x3C:]), -1)
	if err != nil {
		return nil, err
	}
	for i := 0; i+4 <= len(mf); i += 4 {
		f.mini = append(f.mini, le.Uint32(mf[i:]))
	}
	if f.ministrm, err = f.chain(f.dir[0].start, int(f.dir[0].size)); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *cfb) sectorAt(s uint32) []byte {
	off := (int(s) + 1) * f.sector
	if s >= cfbEnd || off < 0 || off+f.sector > len(f.data) {
		return nil
	}
	return f.data[off : off+f.sector]
}

// chain reads the sectors from start on (size < 0 — all of them).
func (f *cfb) chain(start uint32, size int) ([]byte, error) {
	var out []byte
	for s, n := start, 0; s < cfbEnd; n++ {
		b := f.sectorAt(s)
		if b == nil || int(s) >= len(f.fat) || n > len(f.fat) {
			return nil, errNotMsg
		}
		out = append(out, b...)
		if size >= 0 && len(out) >= size {
			break
		}
		s = f.fat[s]
	}
	if size >= 0 {
		if len(out) < size {
			return nil, errNotMsg
		}
		out = out[:size]
	}
	return out, nil
}

// stream is the contents of a stream entry: small ones are in the mini stream, in 64-byte sectors.
func (f *cfb) stream(e cfbEntry) []byte {
	if e.size < 4096 {
		var out []byte
		for s, n := e.start, 0; s < cfbEnd && uint64(len(out)) < e.size; n++ {
			off := int(s) * 64
			if int(s) >= len(f.mini) || off+64 > len(f.ministrm) || n > len(f.mini) {
				return nil
			}
			out = append(out, f.ministrm[off:off+64]...)
			s = f.mini[s]
		}
		if uint64(len(out)) < e.size {
			return nil
		}
		return out[:e.size]
	}
	b, _ := f.chain(e.start, int(e.size))
	return b
}

// children are the entries of a storage: their numbers by name (the directory keeps them as a red-black tree).
func (f *cfb) children(i int) map[string]int {
	out := map[string]int{}
	seen := map[uint32]bool{}
	var walk func(n uint32)
	walk = func(n uint32) {
		if n == cfbNoSibl || int(n) >= len(f.dir) || seen[n] {
			return
		}
		seen[n] = true
		e := f.dir[n]
		out[e.name] = int(n)
		walk(e.left)
		walk(e.right)
	}
	walk(f.dir[i].child)
	return out
}

// mapiProps are the properties of a storage of the letter: the variable ones (strings, binaries) from their
// streams, the fixed ones (numbers, times) from «__properties_version1.0».
type mapiProps struct {
	str  map[uint16]string
	bin  map[uint16][]byte
	num  map[uint16]uint64
	kids map[string]int
}

func (f *cfb) props(i int, header int) mapiProps {
	p := mapiProps{str: map[uint16]string{}, bin: map[uint16][]byte{}, num: map[uint16]uint64{}, kids: f.children(i)}
	for name, k := range p.kids {
		e := f.dir[k]
		if e.kind != 2 || !strings.HasPrefix(name, "__substg1.0_") || len(name) != 20 {
			continue
		}
		var id, typ uint16
		if _, err := fmt.Sscanf(name[12:], "%04X%04X", &id, &typ); err != nil {
			continue
		}
		b := f.stream(e)
		switch typ {
		case 0x001F:
			p.str[id] = utf16le(b)
		case 0x001E:
			p.str[id] = strings.TrimRight(string(b), "\x00")
		case 0x0102:
			p.bin[id] = b
		}
	}
	if k, ok := p.kids["__properties_version1.0"]; ok {
		b := f.stream(f.dir[k])
		for off := header; off+16 <= len(b); off += 16 {
			tag := binary.LittleEndian.Uint32(b[off:])
			switch tag & 0xFFFF {
			case 0x0002, 0x0003, 0x000B, 0x0040, 0x0014:
				p.num[uint16(tag>>16)] = binary.LittleEndian.Uint64(b[off+8:])
			}
		}
	}
	return p
}

func utf16le(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, binary.LittleEndian.Uint16(b[i:]))
	}
	return strings.TrimRight(string(utf16.Decode(u)), "\x00")
}

// fileTime: a FILETIME (100 ns since 1601) as time.
func fileTime(v uint64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(0, 0).Add(time.Duration(v-116444736000000000) * 100)
}

// MAPI property ids the import reads.
const (
	prSubject        = 0x0037
	prSubmitTime     = 0x0039
	prSenderName     = 0x0C1A
	prSenderEmail    = 0x0C1F
	prSenderSMTP     = 0x5D01
	prDeliveryTime   = 0x0E06
	prImportance     = 0x0017
	prHeaders        = 0x007D
	prBody           = 0x1000
	prHTML           = 0x1013
	prMessageID      = 0x1035
	prInReplyTo      = 0x1042
	prRcptType       = 0x0C15
	prDisplayName    = 0x3001
	prEmail          = 0x3003
	prSMTP           = 0x39FE
	prAttachData     = 0x3701
	prAttachFile     = 0x3704
	prAttachLongFile = 0x3707
	prAttachMime     = 0x370E
	prAttachCID      = 0x3712
)

// msgMIME: the letter of an Outlook .msg file as MIME (its own headers when the file keeps them, as it does a
// received letter's).
func msgMIME(data []byte, host string) ([]byte, error) {
	f, err := readCFB(data)
	if err != nil {
		return nil, err
	}
	root := f.props(0, 32)
	if len(root.str) == 0 && len(root.bin) == 0 {
		return nil, errNotMsg
	}
	var hdr mail.Header
	if h := root.str[prHeaders]; h != "" {
		if m, err := mail.ReadMessage(strings.NewReader(strings.TrimRight(h, "\r\n") + "\r\n\r\n")); err == nil {
			hdr = m.Header
		}
	}
	addr := func(name, email string) *mail.Address {
		if email = strings.TrimSpace(email); email == "" {
			return nil
		}
		return &mail.Address{Name: strings.TrimSpace(name), Address: email}
	}
	e := envelope{Host: host}
	if l, err := hdr.AddressList("From"); err == nil && len(l) > 0 {
		e.From = *l[0]
	} else if a := addr(root.str[prSenderName], first(root.str[prSenderSMTP], smtpOnly(root.str[prSenderEmail]))); a != nil {
		e.From = *a
	}
	if t, err := hdr.Date(); err == nil {
		e.Now = t
	} else if t := fileTime(first64(root.num[prSubmitTime], root.num[prDeliveryTime])); !t.IsZero() {
		e.Now = t
	} else {
		e.Now = time.Now()
	}
	e.ID = msgID(first(hdr.Get("Message-Id"), root.str[prMessageID]))
	var to, cc, bcc []*mail.Address
	if hdr != nil {
		to, _ = hdr.AddressList("To")
		cc, _ = hdr.AddressList("Cc")
	}
	if len(to) == 0 && len(cc) == 0 {
		for name, k := range root.kids {
			if f.dir[k].kind != 1 || !strings.HasPrefix(name, "__recip_version1.0_") {
				continue
			}
			r := f.props(k, 8)
			a := addr(r.str[prDisplayName], first(r.str[prSMTP], smtpOnly(r.str[prEmail])))
			if a == nil {
				continue
			}
			switch r.num[prRcptType] & 0xFF {
			case 2:
				cc = append(cc, a)
			case 3:
				bcc = append(bcc, a)
			default:
				to = append(to, a)
			}
		}
	}
	e.To, e.CC, e.BCC, e.WithBCC = to, cc, bcc, true
	l := Letter{Subject: root.str[prSubject], Text: root.str[prBody]}
	if h, ok := root.bin[prHTML]; ok {
		l.HTML = string(h)
	} else if h, ok := root.str[prHTML]; ok {
		l.HTML = h
	}
	l.HTML = bodyOf(l.HTML)
	if l.Text == "" && l.HTML != "" {
		l.Text = HTMLText(l.HTML)
	}
	switch root.num[prImportance] & 0xFF {
	case 2:
		l.Importance = 1
	case 0:
		if _, ok := root.num[prImportance]; ok {
			l.Importance = -1
		}
	}
	if r := msgID(first(hdr.Get("In-Reply-To"), root.str[prInReplyTo])); r != "" {
		l.Refs = []string{r}
	}
	for name, k := range root.kids {
		if f.dir[k].kind != 1 || !strings.HasPrefix(name, "__attach_version1.0_") {
			continue
		}
		a := f.props(k, 8)
		data, ok := a.bin[prAttachData]
		if !ok { // a letter or an object inside the letter: not a file
			continue
		}
		file := Attachment{Name: first(a.str[prAttachLongFile], a.str[prAttachFile], "file"), ContentType: first(a.str[prAttachMime], "application/octet-stream"),
			Data: data, ContentID: strings.Trim(a.str[prAttachCID], "<>")}
		if file.ContentID != "" && !strings.Contains(l.HTML, "cid:"+file.ContentID) {
			file.ContentID = "" // a picture the text does not show is a file of the letter
		}
		l.Files = append(l.Files, file)
	}
	msg, _ := compose(e, l)
	return msg, nil
}

// bodyOf: what is inside <body> of an HTML document (htmlPart wraps it again).
func bodyOf(h string) string {
	low := strings.ToLower(h)
	if i := strings.Index(low, "<body"); i >= 0 {
		if j := strings.IndexByte(low[i:], '>'); j >= 0 {
			h, low = h[i+j+1:], low[i+j+1:]
			if k := strings.LastIndex(low, "</body"); k >= 0 {
				h = h[:k]
			}
		}
	}
	return strings.TrimSpace(h)
}

// smtpOnly drops an Exchange address (/O=…/CN=…) that is no e-mail address.
func smtpOnly(s string) string {
	if strings.Contains(s, "@") {
		return s
	}
	return ""
}

func first(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func first64(v ...uint64) uint64 {
	for _, x := range v {
		if x != 0 {
			return x
		}
	}
	return 0
}

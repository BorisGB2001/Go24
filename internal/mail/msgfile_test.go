package mail

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"mime"
	"net/mail"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// cfbNode is a storage or a stream of a compound file the tests write.
type cfbNode struct {
	name    string
	data    []byte
	kids    []*cfbNode
	storage bool
}

// writeCFB writes a compound file (version 3) as Outlook does: streams under 4096 bytes in the mini stream, the
// others in sectors of their own; the siblings of a storage are chained to the right.
func writeCFB(root *cfbNode) []byte {
	le := binary.LittleEndian
	var nodes []*cfbNode
	var walk func(n *cfbNode)
	walk = func(n *cfbNode) {
		nodes = append(nodes, n)
		for _, k := range n.kids {
			walk(k)
		}
	}
	walk(root)
	index := map[*cfbNode]int{}
	for i, n := range nodes {
		index[n] = i
	}
	pad := func(b []byte, n int) []byte {
		for len(b)%n != 0 {
			b = append(b, 0)
		}
		return b
	}
	// the mini stream and its FAT
	var ministrm []byte
	var minifat []uint32
	ministart := map[*cfbNode]uint32{}
	for _, n := range nodes[1:] {
		if n.storage || len(n.data) >= 4096 {
			continue
		}
		if len(n.data) == 0 {
			ministart[n] = cfbEnd
			continue
		}
		first := uint32(len(ministrm) / 64)
		ministart[n] = first
		k := (len(n.data) + 63) / 64
		for i := 0; i < k; i++ {
			if i == k-1 {
				minifat = append(minifat, cfbEnd)
			} else {
				minifat = append(minifat, first+uint32(i)+1)
			}
		}
		ministrm = append(ministrm, pad(append([]byte{}, n.data...), 64)...)
	}
	mf := make([]byte, 4*len(minifat))
	for i, v := range minifat {
		le.PutUint32(mf[4*i:], v)
	}
	// the blobs in sectors: the directory, the mini FAT, the mini stream, the big streams
	dirSize := len(nodes) * 128
	blobs := [][]byte{make([]byte, dirSize), mf, ministrm}
	var big []*cfbNode
	for _, n := range nodes[1:] {
		if !n.storage && len(n.data) >= 4096 {
			big = append(big, n)
			blobs = append(blobs, n.data)
		}
	}
	secs := func(b []byte) int { return (len(b) + 511) / 512 }
	others := 0
	for _, b := range blobs {
		others += secs(b)
	}
	nfat := 1
	for nfat+others > nfat*128 {
		nfat++
	}
	fat := make([]uint32, nfat*128)
	for i := range fat {
		fat[i] = 0xFFFFFFFF
	}
	for i := 0; i < nfat; i++ {
		fat[i] = 0xFFFFFFFD
	}
	start := make([]uint32, len(blobs))
	next := uint32(nfat)
	for i, b := range blobs {
		k := secs(b)
		if k == 0 {
			start[i] = cfbEnd
			continue
		}
		start[i] = next
		for j := 0; j < k; j++ {
			if j == k-1 {
				fat[next] = cfbEnd
			} else {
				fat[next] = next + 1
			}
			next++
		}
	}
	// the directory
	dir := blobs[0]
	for i, n := range nodes {
		e := dir[128*i:]
		u := utf16.Encode([]rune(n.name))
		for j, c := range u {
			le.PutUint16(e[2*j:], c)
		}
		le.PutUint16(e[64:], uint16(2*(len(u)+1)))
		e[66] = 2
		if n.storage {
			e[66] = 1
		}
		if i == 0 {
			e[66] = 5
		}
		e[67] = 1 // black
		le.PutUint32(e[68:], cfbNoSibl)
		le.PutUint32(e[72:], cfbNoSibl)
		le.PutUint32(e[76:], cfbNoSibl)
		switch {
		case i == 0:
			le.PutUint32(e[116:], start[2])
			le.PutUint64(e[120:], uint64(len(ministrm)))
		case n.storage:
		case len(n.data) >= 4096:
			for j, b := range big {
				if b == n {
					le.PutUint32(e[116:], start[3+j])
				}
			}
			le.PutUint64(e[120:], uint64(len(n.data)))
		default:
			le.PutUint32(e[116:], ministart[n])
			le.PutUint64(e[120:], uint64(len(n.data)))
		}
	}
	for _, n := range nodes {
		if len(n.kids) == 0 {
			continue
		}
		le.PutUint32(dir[128*index[n]+76:], uint32(index[n.kids[0]]))
		for j := 0; j+1 < len(n.kids); j++ {
			le.PutUint32(dir[128*index[n.kids[j]]+72:], uint32(index[n.kids[j+1]]))
		}
	}
	// the header
	h := make([]byte, 512)
	copy(h, []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1})
	le.PutUint16(h[0x18:], 0x3E)
	le.PutUint16(h[0x1A:], 3)
	le.PutUint16(h[0x1C:], 0xFFFE)
	le.PutUint16(h[0x1E:], 9)
	le.PutUint16(h[0x20:], 6)
	le.PutUint32(h[0x2C:], uint32(nfat))
	le.PutUint32(h[0x30:], start[0])
	le.PutUint32(h[0x38:], 4096)
	le.PutUint32(h[0x3C:], start[1])
	le.PutUint32(h[0x40:], uint32(secs(mf)))
	le.PutUint32(h[0x44:], cfbEnd)
	for i := 0; i < 109; i++ {
		v := uint32(0xFFFFFFFF)
		if i < nfat {
			v = uint32(i)
		}
		le.PutUint32(h[0x4C+4*i:], v)
	}
	out := h
	fb := make([]byte, 4*len(fat))
	for i, v := range fat {
		le.PutUint32(fb[4*i:], v)
	}
	out = append(out, fb...)
	for _, b := range blobs {
		out = append(out, pad(append([]byte{}, b...), 512)...)
	}
	return out
}

// mapi is a storage of MAPI properties: strings (unicode), binaries and fixed ones (with their types).
type mapi struct {
	str   map[uint16]string
	bin   map[uint16][]byte
	fixed map[uint32]uint64 // tag (id<<16 | type) → value
	kids  []*cfbNode
}

func (m mapi) node(name string, header int) *cfbNode {
	n := &cfbNode{name: name, storage: true}
	for id, s := range m.str {
		u := utf16.Encode([]rune(s))
		b := make([]byte, 2*len(u))
		for i, c := range u {
			binary.LittleEndian.PutUint16(b[2*i:], c)
		}
		n.kids = append(n.kids, &cfbNode{name: fmt.Sprintf("__substg1.0_%04X001F", id), data: b})
	}
	for id, b := range m.bin {
		n.kids = append(n.kids, &cfbNode{name: fmt.Sprintf("__substg1.0_%04X0102", id), data: b})
	}
	p := make([]byte, header)
	for tag, v := range m.fixed {
		e := make([]byte, 16)
		binary.LittleEndian.PutUint32(e, tag)
		binary.LittleEndian.PutUint64(e[8:], v)
		p = append(p, e...)
	}
	n.kids = append(n.kids, &cfbNode{name: "__properties_version1.0", data: p})
	n.kids = append(n.kids, m.kids...)
	return n
}

func toFileTime(t time.Time) uint64 { return uint64(t.UnixNano()/100) + 116444736000000000 }

// TestMsgMIME: an Outlook letter saved as .msg becomes MIME — subject, sender, recipients by kind, the time it was
// sent, the text and the HTML, files (a big one in sectors of its own, a picture of the text inline); a received
// letter's own headers win; a file that is no compound file is no letter.
func TestMsgMIME(t *testing.T) {
	sent := time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC)
	big := bytes.Repeat([]byte("0123456789"), 1000)
	recip := func(i int, name, smtp string, kind uint64) *cfbNode {
		return mapi{str: map[uint16]string{prDisplayName: name, prEmail: "/O=EXCH/CN=" + name, prSMTP: smtp},
			fixed: map[uint32]uint64{prRcptType<<16 | 0x0003: kind}}.node(fmt.Sprintf("__recip_version1.0_#%08X", i), 8)
	}
	attach := func(i int, name, mimeType, cid string, data []byte) *cfbNode {
		return mapi{str: map[uint16]string{prAttachLongFile: name, prAttachMime: mimeType, prAttachCID: cid},
			bin: map[uint16][]byte{prAttachData: data}}.node(fmt.Sprintf("__attach_version1.0_#%08X", i), 8)
	}
	root := mapi{
		str: map[uint16]string{prSubject: "Счёт № 7", prSenderName: "Борис", prSenderEmail: "/O=EXCH/CN=BORIS", prSenderSMTP: "boris@buyer.test",
			prBody: "Добрый день!\r\nСчёт во вложении."},
		bin:   map[uint16][]byte{prHTML: []byte(`<html><head><style>p{}</style></head><body><p>Добрый день!</p><img src="cid:logo1"></body></html>`)},
		fixed: map[uint32]uint64{prSubmitTime<<16 | 0x0040: toFileTime(sent), prImportance<<16 | 0x0003: 2},
		kids: []*cfbNode{recip(0, "Анна", "anna@go24.test", 1), recip(1, "Пётр", "petr@go24.test", 2),
			attach(0, "счёт.pdf", "application/pdf", "", []byte("%PDF-1.4 счёт")), attach(1, "logo.png", "image/png", "logo1", []byte("\x89PNG")),
			attach(2, "big.txt", "text/plain", "", big)},
	}.node("Root Entry", 32)
	msg, err := msgMIME(writeCFB(root), "go24.test")
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(bytes.NewReader(msg))
	if err != nil {
		t.Fatal(err)
	}
	dec := new(mime.WordDecoder)
	subj, _ := dec.DecodeHeader(m.Header.Get("Subject"))
	from, _ := m.Header.AddressList("From")
	to, _ := m.Header.AddressList("To")
	cc, _ := m.Header.AddressList("Cc")
	date, _ := m.Header.Date()
	if subj != "Счёт № 7" || len(from) != 1 || from[0].Address != "boris@buyer.test" || from[0].Name != "Борис" || len(to) != 1 || to[0].Address != "anna@go24.test" ||
		len(cc) != 1 || cc[0].Address != "petr@go24.test" || !date.Equal(sent) || m.Header.Get("Importance") != "High" {
		t.Fatalf("headers: %q from %v to %v cc %v at %v importance %q", subj, from, to, cc, date, m.Header.Get("Importance"))
	}
	p := parseFull(bytes.NewReader(msg))
	if !strings.Contains(p.body, "Счёт во вложении.") || !strings.Contains(p.html, "<p>Добрый день!</p>") || strings.Contains(p.html, "<style>") {
		t.Fatalf("text %q html %q", p.body, p.html)
	}
	names := map[string]int{}
	for _, f := range p.files {
		names[f.Name] = len(f.Data)
	}
	if names["счёт.pdf"] != len("%PDF-1.4 счёт") || names["big.txt"] != len(big) {
		t.Fatalf("files = %v", names)
	}
	if !bytes.Contains(msg, []byte("Content-Id: <logo1>")) {
		t.Fatal("the picture of the text is not inline")
	}

	// a received letter keeps its own headers in the file
	root = mapi{str: map[uint16]string{prSubject: "Re: заказ", prSenderName: "X", prBody: "Да",
		prHeaders: "From: Клиент <client@buyer.test>\r\nTo: anna@go24.test\r\nDate: Mon, 19 Oct 2026 08:00:00 +0200\r\nMessage-ID: <orig-1@buyer.test>\r\nIn-Reply-To: <q-1@go24.test>\r\n"}}.node("Root Entry", 32)
	msg, err = msgMIME(writeCFB(root), "go24.test")
	if err != nil {
		t.Fatal(err)
	}
	m, _ = mail.ReadMessage(bytes.NewReader(msg))
	if from, _ := m.Header.AddressList("From"); len(from) != 1 || from[0].Address != "client@buyer.test" || m.Header.Get("Message-Id") != "<orig-1@buyer.test>" ||
		m.Header.Get("In-Reply-To") != "<q-1@go24.test>" || !strings.HasPrefix(m.Header.Get("Date"), "Mon, 19 Oct 2026 08:00:00 +0200") {
		t.Fatalf("own headers lost: %v", m.Header)
	}

	if _, err := msgMIME([]byte("From: a@b.test\r\n\r\nhi"), "go24.test"); err == nil {
		t.Fatal("a text is taken for .msg")
	}
	if _, err := msgMIME(writeCFB(&cfbNode{name: "Root Entry", storage: true}), "go24.test"); err == nil {
		t.Fatal("an empty compound file is taken for a letter")
	}
}

// TestLetterOf: a file is a letter (.eml with LF lines, an mbox letter, .msg), or not (a text, an empty file).
func TestLetterOf(t *testing.T) {
	eml := "From: a@buyer.test\nTo: b@go24.test\nSubject: Hi\nDate: Tue, 20 Oct 2026 10:00:00 +0000\n\nline 1\nline 2\n"
	msg, date, ok := letterOf(ImportFile{Name: "a.eml", Data: []byte(eml)}, "go24.test")
	if !ok || !date.Equal(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)) || !bytes.Contains(msg, []byte("+0000\r\n\r\nline 1\r\nline 2\r\n")) || bytes.Contains(msg, []byte("\r\r")) {
		t.Fatalf("eml: %v %v %q", ok, date, msg)
	}
	// headers in UTF-8 as they are become RFC 2047 words
	raw := "From: Клиент <client@buyer.test>\r\nTo: b@go24.test\r\nSubject: Старый\r\n заказ\r\n\r\nтело\r\n"
	if msg, _, ok := letterOf(ImportFile{Name: "u.eml", Data: []byte(raw)}, "go24.test"); !ok || hasNonASCII(msg[:bytes.Index(msg, []byte("\r\n\r\n"))]) {
		t.Fatalf("utf-8 headers: %q", msg)
	} else if m, _ := mail.ReadMessage(bytes.NewReader(msg)); m == nil {
		t.Fatal("not a letter")
	} else if subj, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject")); subj != "Старый заказ" || !bytes.HasSuffix(msg, []byte("\r\n\r\nтело\r\n")) {
		t.Fatalf("subject %q, letter %q", subj, msg)
	} else if from, err := m.Header.AddressList("From"); err != nil || from[0].Name != "Клиент" || from[0].Address != "client@buyer.test" {
		t.Fatalf("from %v %v", from, err)
	}
	if msg, _, ok := letterOf(ImportFile{Name: "b.eml", Data: []byte("From a@buyer.test Tue Oct 20 10:00:00 2026\r\n" + eml)}, "go24.test"); !ok || !bytes.HasPrefix(msg, []byte("From: a@buyer.test\r\n")) {
		t.Fatalf("mbox letter: %v %q", ok, msg)
	}
	for _, f := range []ImportFile{{"notes.eml", []byte("Note: buy milk\n\nand bread")}, {"empty.eml", nil}, {"x.msg", []byte("not a compound file")}} {
		if _, _, ok := letterOf(f, "go24.test"); ok {
			t.Fatalf("%s taken for a letter", f.Name)
		}
	}
	root := mapi{str: map[uint16]string{prSubject: "Из Outlook", prSenderSMTP: "boris@buyer.test", prBody: "Текст"}}.node("Root Entry", 32)
	msg, _, ok = letterOf(ImportFile{Name: "letter.MSG", Data: writeCFB(root)}, "go24.test")
	if !ok {
		t.Fatal(".msg not taken")
	}
	if m, err := mail.ReadMessage(bytes.NewReader(msg)); err != nil || m.Header.Get("From") == "" {
		t.Fatalf(".msg letter: %v", err)
	} else if b, _ := io.ReadAll(m.Body); len(b) == 0 {
		t.Fatal(".msg letter has no body")
	}
}

package pst

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSample writes a file of folders and letters: a small letter, letters with attachments of every size the NDB
// keeps differently (in the heap, a block, an XBLOCK, an XXBLOCK) and a folder of many letters.
func writeSample(t *testing.T, name string, many int) map[string][]byte {
	t.Helper()
	out, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	w, err := Create(out, "go24 — tester@go24.test")
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := w.AddFolder(w.Top(), "Входящие", Inbox)
	if err != nil {
		t.Fatal(err)
	}
	sub, _ := w.AddFolder(inbox, "Проекты", Normal)
	bulk, _ := w.AddFolder(w.Top(), "Архив", Normal)
	w.Rename(w.DeletedItems(), "Корзина")
	when := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	files := map[string][]byte{}
	for i, size := range []int{100, 5000, 20000, 9 << 20} {
		data := bytes.Repeat([]byte{byte('a' + i)}, size)
		files[fmt.Sprintf("file%d.bin", i)] = data
	}
	letter := func(subject string, read bool, atts ...string) *Message {
		m := &Message{Props: []Prop{
			{TagSubject, subject},
			{TagConversationTopic, subject},
			{TagSenderName, "Борис"},
			{TagSenderEmail, "boris@go24.test"},
			{TagSenderAddrType, "SMTP"},
			{TagSentRepresentingName, "Борис"},
			{TagSentRepresentingEmail, "boris@go24.test"},
			{TagSentRepresentingAddrType, "SMTP"},
			{TagClientSubmitTime, when},
			{TagMessageDeliveryTime, when},
			{TagBody, "Текст письма " + subject},
			{TagMessageSize, 1234},
		}, Recipients: []Recipient{{Type: RecipientTo, Name: "Тестов Тест", Email: "tester@go24.test"},
			{Type: RecipientCc, Email: "copy@go24.test"}}}
		if read {
			m.Props = append(m.Props, Prop{TagMessageFlags, FlagRead})
		}
		for _, a := range atts {
			m.Attachments = append(m.Attachments, Attachment{Filename: a, MimeType: "application/octet-stream", Data: files[a]})
		}
		return m
	}
	if err := w.AddMessage(inbox, letter("Привет", false)); err != nil {
		t.Fatal(err)
	}
	if err := w.AddMessage(sub, letter("Вложения", true, "file0.bin", "file1.bin", "file2.bin")); err != nil {
		t.Fatal(err)
	}
	if err := w.AddMessage(sub, letter("Большое вложение", true, "file3.bin")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < many; i++ {
		if err := w.AddMessage(bulk, letter(fmt.Sprintf("Письмо %d", i), i%2 == 0)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return files
}

func TestWriteRead(t *testing.T) {
	name := filepath.Join(t.TempDir(), "sample.pst")
	if dir := os.Getenv("PST_KEEP"); dir != "" {
		name = filepath.Join(dir, "sample.pst")
	}
	files := writeSample(t, name, 3000)
	fh, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	f, err := Open(fh)
	if err != nil {
		t.Fatal(err)
	}
	checkAll(t, f)
	store, err := f.Node(nidMessageStore)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := store.Props()
	if err != nil {
		t.Fatal(err)
	}
	if got := sp.String(TagDisplayName); got != "go24 — tester@go24.test" {
		t.Fatalf("store name %q", got)
	}
	// the folders by their names, through the hierarchy tables from the root
	byName := map[string]uint32{}
	var walk func(nid uint32, depth int)
	walk = func(nid uint32, depth int) {
		ht, err := f.Node(makeNID(nidTypeHierarchyTable, nid>>5))
		if err != nil {
			t.Fatal(err)
		}
		tb, err := ht.Table()
		if err != nil {
			t.Fatal(err)
		}
		for i, r := range tb.Rows {
			id := tb.RowIDs[i]
			byName[r.String(TagDisplayName)] = id
			if nidType(id) == nidTypeNormalFolder && depth < 5 {
				walk(id, depth+1)
			}
		}
	}
	walk(nidRootFolder, 0)
	for _, n := range []string{"Top of Personal Folders", "Search Root", "SPAM Search Folder 2", "Корзина", "Входящие", "Проекты", "Архив"} {
		if byName[n] == 0 {
			t.Fatalf("no folder %q in %v", n, byName)
		}
	}
	contents := func(folder string) *Table {
		n, err := f.Node(makeNID(nidTypeContentsTable, byName[folder]>>5))
		if err != nil {
			t.Fatal(err)
		}
		tb, err := n.Table()
		if err != nil {
			t.Fatal(err)
		}
		return tb
	}
	archive := contents("Архив")
	if len(archive.Rows) != 3000 {
		t.Fatalf("Архив: %d rows", len(archive.Rows))
	}
	for i, r := range archive.Rows {
		if got, want := r.Subject(), fmt.Sprintf("Письмо %d", i); got != want {
			t.Fatalf("row %d: %q, not %q", i, got, want)
		}
	}
	fp, _ := f.Node(byName["Архив"])
	ap, _ := fp.Props()
	if ap.Int(TagContentCount) != 3000 || ap.Int(TagContentUnreadCount) != 1500 {
		t.Fatalf("Архив: count %d unread %d", ap.Int(TagContentCount), ap.Int(TagContentUnreadCount))
	}
	proj := contents("Проекты")
	if len(proj.Rows) != 2 {
		t.Fatalf("Проекты: %d rows", len(proj.Rows))
	}
	got := map[string][]byte{}
	for _, id := range proj.RowIDs {
		if f.Parent(id) != byName["Проекты"] {
			t.Fatalf("letter %#x: parent %#x", id, f.Parent(id))
		}
		m, err := f.Node(id)
		if err != nil {
			t.Fatal(err)
		}
		mp, err := m.Props()
		if err != nil {
			t.Fatal(err)
		}
		if mp.Int(TagMessageFlags)&FlagHasAttach == 0 || mp.String(TagDisplayTo) != "Тестов Тест" || mp.String(TagDisplayCc) != "copy@go24.test" {
			t.Fatalf("letter %q: flags %d to %q cc %q", mp.Subject(), mp.Int(TagMessageFlags), mp.String(TagDisplayTo), mp.String(TagDisplayCc))
		}
		rn, err := m.Sub(nidRecipientTableTemplate)
		if err != nil {
			t.Fatal(err)
		}
		rt, err := rn.Table()
		if err != nil || len(rt.Rows) != 2 || rt.Rows[0].String(TagEmailAddress) != "tester@go24.test" {
			t.Fatalf("recipients: %v %+v", err, rt)
		}
		an, err := m.Sub(nidAttachmentTableTemplate)
		if err != nil {
			t.Fatal(err)
		}
		at, err := an.Table()
		if err != nil {
			t.Fatal(err)
		}
		for i, r := range at.Rows {
			a, err := m.Sub(at.RowIDs[i])
			if err != nil {
				t.Fatal(err)
			}
			pa, err := a.Props()
			if err != nil {
				t.Fatal(err)
			}
			got[r.String(TagAttachFilename)] = pa[TagAttachDataBinary]
		}
	}
	for n, data := range files {
		if !bytes.Equal(got[n], data) {
			t.Fatalf("attachment %s: %d bytes, not %d", n, len(got[n]), len(data))
		}
	}
}

// checkAll reads every node of the file, its subnodes, its properties or table: every block's CRC is checked on the way.
func checkAll(t *testing.T, f *File) {
	t.Helper()
	var check func(n *Node, depth int)
	check = func(n *Node, depth int) {
		if len(n.data) > 0 && len(n.data[0]) > 3 && n.data[0][2] == hnSig {
			var err error
			switch n.data[0][3] {
			case bTypePC:
				_, err = n.Props()
			case bTypeTC:
				_, err = n.Table()
			}
			if err != nil {
				t.Fatalf("node %#x: %v", n.NID, err)
			}
		}
		for _, s := range n.SubNIDs() {
			sn, err := n.Sub(s)
			if err != nil {
				t.Fatalf("node %#x: sub %#x: %v", n.NID, s, err)
			}
			if depth < 4 {
				check(sn, depth+1)
			}
		}
	}
	for _, nid := range f.NIDs() {
		n, err := f.Node(nid)
		if err != nil {
			t.Fatalf("node %#x: %v", nid, err)
		}
		check(n, 0)
	}
}

func TestEncodeSubject(t *testing.T) {
	for in, want := range map[string]string{
		"Привет":        "\x01\x01Привет",
		"RE: Привет":    "\x01\x05RE: Привет",
		"Отв: тема":     "\x01\x06Отв: тема",
		"Fwd:тема":      "\x01\x05Fwd:тема",
		"Встреча: в 10": "\x01\x01Встреча: в 10",
		"12: время":     "\x01\x0112: время",
		"":              "\x01\x01",
	} {
		if got := encodeSubject(in); got != want {
			t.Errorf("%q: %q, not %q", in, got, want)
		}
	}
}

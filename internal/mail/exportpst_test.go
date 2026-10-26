package mail

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"

	"go24/internal/pst"
)

const pstSample = "From: =?utf-8?B?0JHQvtGA0LjRgQ==?= <boris@go24.test>\r\n" +
	"To: Ира <ira@go24.test>, oleg@go24.test\r\n" +
	"Cc: copy@go24.test\r\n" +
	"Subject: =?koi8-r?B?8NLJ18XU?=\r\n" + // «Привет» in koi8-r
	"Date: Mon, 05 Oct 2026 10:00:00 +0300\r\n" +
	"Message-ID: <a1@go24.test>\r\n" +
	"In-Reply-To: <a0@go24.test>\r\n" +
	"X-Priority: 1\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"m\"\r\n\r\n" +
	"--m\r\nContent-Type: multipart/related; boundary=\"r\"\r\n\r\n" +
	"--r\r\nContent-Type: multipart/alternative; boundary=\"a\"\r\n\r\n" +
	"--a\r\nContent-Type: text/plain; charset=koi8-r\r\nContent-Transfer-Encoding: base64\r\n\r\n1MXL09QgzMnT1ME=\r\n" + // «текст листа»
	"--a\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>текст <img src=\"cid:pic1\"></p>\r\n" +
	"--a--\r\n" +
	"--r\r\nContent-Type: image/png\r\nContent-ID: <pic1>\r\nContent-Transfer-Encoding: base64\r\n\r\niVBORw0KGgo=\r\n" +
	"--r--\r\n" +
	"--m\r\nContent-Type: application/pdf; name=\"счёт.pdf\"\r\nContent-Disposition: attachment; filename*=utf-8''%D1%81%D1%87%D1%91%D1%82.pdf\r\n" +
	"Content-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQ=\r\n" +
	"--m--\r\n"

func TestPSTLetter(t *testing.T) {
	when := time.Date(2026, 10, 5, 7, 5, 0, 0, time.UTC)
	m := pstLetter([]byte(pstSample), &imap.Message{InternalDate: when, Flags: []string{imap.SeenFlag, imap.FlaggedFlag, imap.AnsweredFlag}},
		"ira@go24.test")
	props := map[uint32]any{}
	for _, p := range m.Props {
		props[p.Tag] = p.Value
	}
	for tag, want := range map[uint32]any{
		pst.TagSubject:               "Привет",
		pst.TagSentRepresentingName:  "Борис",
		pst.TagSentRepresentingEmail: "boris@go24.test",
		pst.TagSenderEmail:           "boris@go24.test",
		pst.TagBody:                  "текст листа",
		pst.TagMessageFlags:          pst.FlagRead,
		pst.TagFlagStatus:            2,
		pst.TagLastVerbExecuted:      102,
		pst.TagImportance:            2,
		pst.TagInternetMessageID:     "<a1@go24.test>",
		pst.TagInReplyTo:             "<a0@go24.test>",
		pst.TagMessageToMe:           true,
		pst.TagMessageCcMe:           false,
		pst.TagMessageDeliveryTime:   when,
	} {
		if got := props[tag]; strings.TrimSpace(toString(got)) != toString(want) {
			t.Errorf("%#08x = %v, want %v", tag, got, want)
		}
	}
	if d, _ := props[pst.TagClientSubmitTime].(time.Time); !d.Equal(time.Date(2026, 10, 5, 7, 0, 0, 0, time.UTC)) {
		t.Errorf("submit time %v", d)
	}
	if h, _ := props[pst.TagHTML].([]byte); !strings.Contains(string(h), `cid:pic1`) {
		t.Errorf("html %q", h)
	}
	if hd, _ := props[pst.TagTransportHeaders].(string); !strings.HasPrefix(hd, "From: ") || !strings.HasSuffix(hd, "boundary=\"m\"\r\n") {
		t.Errorf("headers %q", hd)
	}
	if len(m.Recipients) != 3 || m.Recipients[0] != (pst.Recipient{Type: pst.RecipientTo, Name: "Ира", Email: "ira@go24.test"}) ||
		m.Recipients[2].Type != pst.RecipientCc {
		t.Errorf("recipients %+v", m.Recipients)
	}
	if len(m.Attachments) != 2 {
		t.Fatalf("attachments %+v", m.Attachments)
	}
	pic, pdf := m.Attachments[0], m.Attachments[1]
	if pic.ContentID != "pic1" || !pic.Inline || pic.MimeType != "image/png" || pic.Filename != "image.png" || len(pic.Data) != 8 {
		t.Errorf("picture %+v", pic)
	}
	if pdf.Filename != "счёт.pdf" || pdf.Inline || pdf.MimeType != "application/pdf" || string(pdf.Data) != "%PDF-1.4" {
		t.Errorf("file %+v", pdf)
	}

	// not a letter at all: it goes whole as a file
	m = pstLetter([]byte("garbage without headers"), &imap.Message{Envelope: &imap.Envelope{Subject: "x"}}, "")
	if len(m.Attachments) != 1 || m.Attachments[0].Filename != "message.eml" {
		t.Errorf("garbage: %+v", m.Attachments)
	}
}

func toString(v any) string {
	switch x := v.(type) {
	case time.Time:
		return x.UTC().Format(time.RFC3339)
	case []byte:
		return string(x)
	}
	return fmt.Sprint(v)
}

// readPST opens a .pst written by an export: its folders by path and the subjects of their letters.
func readPST(t *testing.T, name string) (map[string][]string, *pst.File) {
	t.Helper()
	fh, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fh.Close() })
	f, err := pst.Open(fh)
	if err != nil {
		t.Fatal(err)
	}
	folders, err := f.Folders()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for path, ids := range folders {
		out[path] = []string{}
		for _, id := range ids {
			l, err := f.Letter(id)
			if err != nil {
				t.Fatal(err)
			}
			out[path] = append(out[path], l.Props.Subject())
		}
	}
	return out, f
}

func tempPST(t *testing.T) *os.File {
	f, err := os.Create(filepath.Join(t.TempDir(), "export.pst"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

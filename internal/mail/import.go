package mail

import (
	"bytes"
	"context"
	"mime"
	"net/mail"
	"path"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"

	"go24/internal/i18n"
)

// MaxImportFiles bounds the files of one «Импорт писем».
const MaxImportFiles = 100

// ImportFile is a letter saved as a file: .eml (as a mail server keeps it) or .msg (Outlook's).
type ImportFile struct {
	Name string
	Data []byte
}

// Imported is what an import did: the letters put into the folder, the files that are no letters.
type Imported struct {
	Done    int
	Skipped []string
}

// Import puts letters saved as files into a folder of the user's mailbox, as Outlook does with files dragged onto a
// folder: on the server (read, with the letter's own date), then the folder is fetched. The import is no new mail:
// no notification, no rule.
func (s *Store) Import(ctx context.Context, userID, folderID string, files []ImportFile) (Imported, error) {
	var out Imported
	to, err := s.Folder(ctx, userID, folderID)
	if err != nil {
		return out, err
	}
	if !to.Selectable || to.Local {
		return out, i18n.E("err.mail_import_folder")
	}
	if len(files) > MaxImportFiles {
		return out, i18n.E("err.mail_import_many", MaxImportFiles)
	}
	a, err := s.account(ctx, to.MailboxID)
	if err != nil {
		return out, err
	}
	host := a.Email[strings.LastIndex(a.Email, "@")+1:]
	type letter struct {
		msg  []byte
		date time.Time
	}
	var letters []letter
	for _, f := range files {
		msg, date, ok := letterOf(f, host)
		if !ok {
			out.Skipped = append(out.Skipped, f.Name)
			continue
		}
		letters = append(letters, letter{msg, date})
	}
	if len(letters) == 0 {
		return out, nil
	}
	err = s.remote(ctx, to.MailboxID, func(c *client.Client, a account) error {
		for _, l := range letters {
			if err := c.Append(to.Name, []string{imap.SeenFlag}, l.date, bytes.NewReader(l.msg)); err != nil {
				return serverErr(err)
			}
			out.Done++
		}
		return s.syncFolder(ctx, c, a, to)
	})
	return out, err
}

// letterOf: the letter of a file and its date; not ok — the file is no letter (or too big for one).
func letterOf(f ImportFile, host string) (msg []byte, date time.Time, ok bool) {
	if len(f.Data) == 0 || len(f.Data) > MaxSendSize*2 {
		return nil, time.Time{}, false
	}
	msg = f.Data
	if strings.EqualFold(path.Ext(f.Name), ".msg") || bytes.HasPrefix(msg, []byte{0xD0, 0xCF, 0x11, 0xE0}) {
		var err error
		if msg, err = msgMIME(f.Data, host); err != nil {
			return nil, time.Time{}, false
		}
	}
	msg = bytes.TrimPrefix(msg, []byte("\xEF\xBB\xBF"))
	if bytes.HasPrefix(msg, []byte("From ")) { // an mbox letter: its separator line is no header
		if i := bytes.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[i+1:]
		}
	}
	m, err := mail.ReadMessage(bytes.NewReader(msg))
	if err != nil {
		return nil, time.Time{}, false
	}
	known := 0
	for _, k := range []string{"From", "To", "Subject", "Date", "Message-Id"} {
		if m.Header.Get(k) != "" {
			known++
		}
	}
	if known < 2 { // some text with a colon in its first line is no letter
		return nil, time.Time{}, false
	}
	if date, err = m.Header.Date(); err != nil {
		date = time.Now()
	}
	// IMAP takes lines ending in CRLF; a file saved on Linux or a Mac may end them in LF only
	msg = bytes.ReplaceAll(bytes.ReplaceAll(msg, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))
	return asciiHeaders(msg), date, true
}

// asciiHeaders encodes the headers written in UTF-8 as they are (RFC 6532) as RFC 2047 words: a server without
// SMTPUTF8 shows them as «????» (GreenMail, Dovecot's envelope).
func asciiHeaders(msg []byte) []byte {
	end := bytes.Index(msg, []byte("\r\n\r\n"))
	if end < 0 || !hasNonASCII(msg[:end]) {
		return msg
	}
	var out bytes.Buffer
	lines := strings.Split(string(msg[:end]), "\r\n")
	for i := 0; i < len(lines); {
		field := lines[i] // with its folded lines
		for i++; i < len(lines) && (strings.HasPrefix(lines[i], " ") || strings.HasPrefix(lines[i], "\t")); i++ {
			field += " " + strings.TrimLeft(lines[i], " \t")
		}
		k, v, ok := strings.Cut(field, ":")
		if ok && hasNonASCII([]byte(v)) {
			v = strings.TrimSpace(v)
			switch strings.ToLower(k) {
			case "from", "to", "cc", "bcc", "reply-to", "sender":
				if list, err := mail.ParseAddressList(v); err == nil {
					v = addrHeader(list)
					break
				}
				v = mime.QEncoding.Encode("utf-8", v)
			default:
				v = mime.QEncoding.Encode("utf-8", v)
			}
			field = k + ": " + v
		}
		out.WriteString(field + "\r\n")
	}
	out.Write(msg[end+2:])
	return out.Bytes()
}

func hasNonASCII(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 {
			return true
		}
	}
	return false
}

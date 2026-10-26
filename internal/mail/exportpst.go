package mail

import (
	"bytes"
	"context"
	"io"
	"net/mail"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // letters in koi8-r, windows-1251 and the like
	gomail "github.com/emersion/go-message/mail"

	"go24/internal/i18n"
	"go24/internal/pst"
)

// «Экспорт» into Outlook's personal folders file (.pst): the folders keep their tree under «Top of Personal Folders»,
// «Входящие», «Отправленные», «Черновики», «Нежелательная почта» become Outlook's own and «Удалённые» its «Deleted
// Items»; each letter becomes a letter of Outlook with its sender, recipients, dates, text, HTML, headers, attachments
// and flags.

// pstSpecial: the folders of roles Outlook knows.
var pstSpecial = map[string]pst.Special{"inbox": pst.Inbox, "sent": pst.Sent, "drafts": pst.Drafts, "junk": pst.Junk}

// writePST writes the export's letters into a .pst.
func (s *Store) writePST(ctx context.Context, e Export, folders []Folder, out pst.Output, progress func()) error {
	w, err := pst.Create(out, e.Email)
	if err != nil {
		return err
	}
	title := func(f Folder) string {
		if f.Role != "" && e.Tr != nil {
			return e.Tr("mail.folder." + f.Role)
		}
		return f.Title()
	}
	// a folder's place: under the folder of its path's beginning, else (that one is not exported) under the top
	made := map[string]pst.Folder{}
	place := func(f Folder) (pst.Folder, error) {
		if f.Role == "trash" {
			w.Rename(w.DeletedItems(), title(f))
			made[f.Name] = w.DeletedItems()
			return w.DeletedItems(), nil
		}
		parent := w.Top()
		if f.Delim != "" && f.Role != "inbox" {
			for p := f.Name; strings.Contains(p, f.Delim); {
				p = p[:strings.LastIndex(p, f.Delim)]
				if id, ok := made[p]; ok {
					parent = id
					break
				}
			}
		}
		id, err := w.AddFolder(parent, title(f), pstSpecial[f.Role])
		made[f.Name] = id
		return id, err
	}
	err = s.remote(ctx, e.MailboxID, func(c *client.Client, _ account) error {
		for _, f := range folders {
			if err := ctx.Err(); err != nil {
				return err
			}
			id, err := place(f)
			if err != nil {
				return err
			}
			if err := s.eachLetter(c, e, f, func(m *imap.Message, r io.Reader) error {
				raw, err := io.ReadAll(r)
				if err != nil {
					return err
				}
				if err := w.AddMessage(id, pstLetter(raw, m, e.Email)); err != nil {
					return err
				}
				progress()
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return w.Close()
}

// pstLetter makes a letter of Outlook of a letter as the server keeps it; own is the mailbox's address (the letters
// to it are «to me»).
func pstLetter(raw []byte, m *imap.Message, own string) *pst.Message {
	out := &pst.Message{}
	add := func(tag uint32, v any) { out.Props = append(out.Props, pst.Prop{Tag: tag, Value: v}) }
	received := m.InternalDate
	if received.IsZero() {
		received = time.Now()
	}
	add(pst.TagMessageDeliveryTime, received)
	add(pst.TagCreationTime, received)
	add(pst.TagLastModificationTime, received)
	add(pst.TagMessageSize, len(raw))
	add(pst.TagInternetCodepage, 65001)
	add(pst.TagMessageCodepage, 65001)
	add(pst.TagMessageFlags, pstFlags(m.Flags))
	for _, fl := range m.Flags {
		switch {
		case fl == imap.FlaggedFlag:
			add(pst.TagFlagStatus, 2) // followupFlagged
		case fl == imap.AnsweredFlag:
			add(pst.TagLastVerbExecuted, 102) // EXCHIVERB_REPLYTOSENDER
		case strings.EqualFold(fl, "$Forwarded"):
			add(pst.TagLastVerbExecuted, 104) // EXCHIVERB_FORWARD
		}
	}
	head := raw
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		head = raw[:i+2]
	} else if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		head = raw[:i+1]
	}
	add(pst.TagTransportHeaders, string(head))
	mr, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil { // not a letter we can read: it goes whole, as a file of the letter
		subject := ""
		if m.Envelope != nil {
			subject = m.Envelope.Subject
		}
		add(pst.TagSubject, subject)
		out.Attachments = append(out.Attachments, pst.Attachment{Filename: "message.eml", MimeType: "message/rfc822", Data: raw})
		return out
	}
	h := mr.Header
	subject, _ := h.Subject()
	add(pst.TagSubject, subject)
	if d, err := h.Date(); err == nil && !d.IsZero() {
		add(pst.TagClientSubmitTime, d)
	} else {
		add(pst.TagClientSubmitTime, received)
	}
	from, _ := h.AddressList("From")
	sender, _ := h.AddressList("Sender")
	if len(sender) == 0 {
		sender = from
	}
	if len(from) > 0 {
		add(pst.TagSentRepresentingName, nameOf(from[0]))
		add(pst.TagSentRepresentingEmail, from[0].Address)
	}
	if len(sender) > 0 {
		add(pst.TagSenderName, nameOf(sender[0]))
		add(pst.TagSenderEmail, sender[0].Address)
	}
	toMe, ccMe := false, false
	for _, part := range []struct {
		header string
		typ    int
	}{{"To", pst.RecipientTo}, {"Cc", pst.RecipientCc}, {"Bcc", pst.RecipientBcc}} {
		list, _ := h.AddressList(part.header)
		for _, a := range list {
			out.Recipients = append(out.Recipients, pst.Recipient{Type: part.typ, Name: a.Name, Email: a.Address})
			if strings.EqualFold(a.Address, own) {
				toMe, ccMe = toMe || part.typ == pst.RecipientTo, ccMe || part.typ == pst.RecipientCc
			}
		}
	}
	add(pst.TagMessageToMe, toMe)
	add(pst.TagMessageCcMe, ccMe)
	if id := strings.TrimSpace(h.Get("Message-Id")); id != "" {
		add(pst.TagInternetMessageID, id)
	}
	if v := strings.TrimSpace(h.Get("In-Reply-To")); v != "" {
		add(pst.TagInReplyTo, v)
	}
	if v := strings.TrimSpace(h.Get("References")); v != "" {
		add(pst.TagInternetReferences, v)
	}
	add(pst.TagImportance, 1+importanceOf(h.Get("Importance"), h.Get("X-Priority"), h.Get("Priority")))

	var plain, html []byte
	gotPlain, gotHTML := false, false
	var files []pst.Attachment
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil && !message.IsUnknownCharset(err) {
			break
		}
		if p == nil {
			continue
		}
		body, _ := io.ReadAll(p.Body)
		switch ph := p.Header.(type) {
		case *gomail.InlineHeader:
			ct, params, _ := ph.ContentType()
			switch {
			case ct == "text/plain" && !gotPlain:
				plain, gotPlain = body, true
			case ct == "text/html" && !gotHTML:
				html, gotHTML = body, true
			default: // a picture of the text, or another part shown inline
				_, dp, _ := ph.ContentDisposition()
				name := dp["filename"]
				if name == "" {
					name = params["name"]
				}
				cid := strings.Trim(ph.Get("Content-Id"), "<> ")
				files = append(files, pst.Attachment{Filename: partName(name, ct), MimeType: ct, ContentID: cid,
					Inline: cid != "" && strings.HasPrefix(ct, "image/"), Data: body})
			}
		case *gomail.AttachmentHeader:
			name, _ := ph.Filename()
			ct, _, _ := ph.ContentType()
			cid := strings.Trim(ph.Get("Content-Id"), "<> ")
			files = append(files, pst.Attachment{Filename: partName(name, ct), MimeType: ct, ContentID: cid, Data: body})
		}
	}
	if gotHTML {
		for i := range files { // a picture the HTML shows is not an attachment to list
			if c := files[i].ContentID; c != "" && strings.HasPrefix(files[i].MimeType, "image/") && bytes.Contains(html, []byte("cid:"+c)) {
				files[i].Inline = true
			}
		}
		add(pst.TagHTML, html)
		if !gotPlain {
			plain = []byte(HTMLText(string(html)))
		}
	}
	add(pst.TagBody, strings.ToValidUTF8(string(plain), "�"))
	out.Attachments = files
	return out
}

// pstFlags are Outlook's flags of the letter's flags on the server.
func pstFlags(flags []string) int {
	out := 0
	for _, f := range flags {
		switch f {
		case imap.SeenFlag:
			out |= pst.FlagRead
		case imap.DraftFlag:
			out |= pst.FlagUnsent
		}
	}
	return out
}

func nameOf(a *mail.Address) string {
	if a.Name != "" {
		return a.Name
	}
	return a.Address
}

// partName is a file name of a part that has none: by its type.
func partName(name, ct string) string {
	if name != "" {
		return name
	}
	switch {
	case ct == "message/rfc822":
		return "message.eml"
	case ct == "text/calendar":
		return "invite.ics"
	case strings.HasPrefix(ct, "image/"):
		return "image." + strings.TrimPrefix(strings.SplitN(ct, "+", 2)[0], "image/")
	}
	return "attachment"
}

// errNoPSTOutput: a .pst is written into a file only (its blocks go at their places).
var errNoPSTOutput = i18n.E("err.mail_export_format")

package web

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/mail"
)

// ---------- «Почта» ----------

// The mail is laid out as in Outlook: the mailboxes and their folders on the left («Все входящие» on top when
// there are several), the letters of a folder in the middle, the reading pane on the right, below or off (cookie
// mail_pane, set by static/js/mail.js). /mail?folder=ID|all&letter=ID&q=…; ?new=1 — a new letter in the pane; the
// list's tabs, filters, order and searches — mailsearch.go.

// mailBox is a mailbox with its folders for the folder pane.
type mailBox struct {
	mail.Mailbox
	Folders []mail.Folder
}

// mailPanes are the places of the reading pane.
var mailPanes = []string{"right", "bottom", "off"}

// mailConv: the list shows conversations (Outlook's default) unless the user turned them off (cookie mail_conv).
func mailConv(r *http.Request) bool {
	c, err := r.Cookie("mail_conv")
	return err != nil || c.Value != "off"
}

// mailCols are the sizes of the panes the user dragged (cookie mail_cols: the folders' and the list's widths, the
// list's height under a reading pane at the bottom), kept within bounds.
type mailCols struct{ Nav, List, ListH int }

func mailColsOf(r *http.Request) mailCols {
	c := mailCols{250, 380, 320}
	if k, err := r.Cookie("mail_cols"); err == nil {
		v := strings.Split(k.Value, ".")
		get := func(i, lo, hi int, to *int) {
			if i < len(v) {
				if n, err := strconv.Atoi(v[i]); err == nil {
					*to = min(max(n, lo), hi)
				}
			}
		}
		get(0, 160, 480, &c.Nav)
		get(1, 240, 900, &c.List)
		get(2, 120, 1200, &c.ListH)
	}
	return c
}

func mailPane(r *http.Request) string {
	if p := r.URL.Query().Get("pane"); p == "off" { // a letter opened in its own window
		return p
	}
	if c, err := r.Cookie("mail_pane"); err == nil {
		for _, p := range mailPanes {
			if c.Value == p {
				return p
			}
		}
	}
	return "right"
}

// mailURL is the address of a folder (and a letter of it) in the mail.
func mailURL(folder, letter string, extra ...string) string {
	v := url.Values{}
	if folder != "" {
		v.Set("folder", folder)
	}
	if letter != "" {
		v.Set("letter", letter)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		v.Set(extra[i], extra[i+1])
	}
	if len(v) == 0 {
		return "/mail"
	}
	return "/mail?" + v.Encode()
}

// mailErr: a form sent in the background gets the message; otherwise back to the page with it.
func (s *Server) mailErr(w http.ResponseWriter, r *http.Request, err error, back string) {
	var ie *i18n.Error
	if fromForm(r) || !errors.As(err, &ie) || ie.Key == "err.not_found" || ie.Key == "err.forbidden" {
		s.taskErr(w, r, err, back)
		return
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	http.Redirect(w, r, back+sep+"msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
}

func (s *Server) mailPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	boxes, err := s.Mail.Mailboxes(ctx, sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	folders, err := s.Mail.Folders(ctx, sess.UserID, "")
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	q := r.URL.Query()
	list := make([]mailBox, len(boxes))
	unseen := 0
	inboxOf := map[string]string{}
	for i, b := range boxes {
		list[i].Mailbox = b
		unseen += b.Unseen
		for _, f := range folders {
			if f.MailboxID == b.ID {
				list[i].Folders = append(list[i].Folders, f)
				if f.Role == "inbox" {
					inboxOf[b.ID] = f.ID
				}
			}
		}
	}
	fid := q.Get("folder")
	if fid == "" {
		switch {
		case q.Get("box") != "":
			fid = inboxOf[q.Get("box")]
		case len(boxes) > 1:
			fid = mail.AllInboxes
		case len(boxes) == 1:
			fid = inboxOf[boxes[0].ID]
		}
	}
	cats, err := s.mailCategories(r, sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	conv := mailConv(r) && fid != mail.SnoozedFolder
	prefs, err := s.Mail.Prefs(ctx, sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	searches, err := s.Mail.Searches(ctx, sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	quick, err := s.mailQuickSteps(r, sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	favs, favSet, err := s.mailFavs(ctx, sess.UserID, list, cats, searches)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	h, err := s.Mail.Handling(ctx, sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	lay, err := s.Mail.Layout(ctx, sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	data := map[string]any{"AfterMove": h.AfterMove, "Layout": lay, "QuickSteps": quick, "Boxes": list, "FolderID": fid, "Searches": searches, "Favs": favs, "FavSet": favSet, "Filters": mail.Filters, "Sorts": mail.Sorts, "New": q.Get("new") == "1", "Pane": mailPane(r),
		"Panes": mailPanes, "Cols": mailColsOf(r), "Unseen": unseen, "Categories": cats, "CatByID": catMap(cats), "Colors": mail.Colors, "Conv": conv, "ConvOn": mailConv(r),
		"SnoozePresets": snoozePresets, "FlagPresets": flagPresets, "RemindPresets": remindPresets, "Back": r.URL.RequestURI(), "Mode": q.Get("mode")}
	var box *mail.Mailbox // the mailbox a new letter goes from
	if len(boxes) > 0 {
		box = &boxes[0]
	}
	if strings.HasPrefix(fid, mail.CategoryFolder) {
		c, err := s.Mail.Category(ctx, sess.UserID, fid[len(mail.CategoryFolder):])
		if err != nil {
			s.taskErr(w, r, err, "/mail")
			return
		}
		data["Category"] = c
	}
	role := ""
	if strings.HasPrefix(fid, mail.SearchFolder) {
		sv, err := s.Mail.SavedSearch(ctx, sess.UserID, fid[len(mail.SearchFolder):])
		if err != nil {
			s.taskErr(w, r, err, "/mail")
			return
		}
		data["Search"] = sv
	}
	if fid != "" && !mail.Virtual(fid) {
		f, err := s.Mail.Folder(ctx, sess.UserID, fid)
		if err != nil {
			s.taskErr(w, r, err, "/mail")
			return
		}
		data["Folder"] = f
		role = f.Role
		for i := range boxes {
			if boxes[i].ID == f.MailboxID {
				box = &boxes[i]
			}
		}
	}
	ml := mailList{ListOpts: mail.ListOpts{Folder: fid, Sort: "date"}}
	if fid != "" {
		ml = mailListOf(w, r, fid, role, prefs.Focused, conv)
		if data["Messages"], err = s.Mail.ListBy(ctx, sess.UserID, ml.ListOpts); err != nil {
			s.taskErr(w, r, err, "/mail")
			return
		}
		if lay.ShowFiles {
			if data["RowFiles"], err = s.mailRowFiles(ctx, sess.UserID, data["Messages"].([]mail.Message)); err != nil {
				s.taskErr(w, r, err, "/mail")
				return
			}
		}
		if ml.Focus && ml.View == mail.ViewFocused {
			if data["OtherN"], data["OtherFrom"], err = s.Mail.OtherNews(ctx, sess.UserID, fid); err != nil {
				s.taskErr(w, r, err, "/mail")
				return
			}
		}
		if ml.Q != "" && ml.Saved == "" {
			if err := s.Mail.Remember(ctx, sess.UserID, ml.Q); err != nil {
				s.Log.Warn("mail recent search", "err", err)
			}
		}
		if ml.Q != "" {
			data["Terms"] = mail.ParseQuery(ml.Q).Terms()
		}
	}
	data["List"], data["Q"], data["Adv"] = ml, ml.Q, mail.ParseQuery(ml.Q).Advanced()
	if ml.Saved != "" { // a search folder's search is not in the search box
		data["Q"], data["Adv"] = "", mail.Advanced{}
	}
	if id := q.Get("letter"); id != "" && q.Get("new") != "1" {
		read, err := s.mailRead(r, sess, id)
		if err != nil {
			s.taskErr(w, r, err, mailURL(fid, ""))
			return
		}
		for k, v := range read {
			data[k] = v
		}
		m := read["Letter"].(mail.Message)
		if ok, err := s.Mail.AdoptDraft(r.Context(), sess.UserID, m.ID); err != nil { // one from the phone too
			s.taskErr(w, r, err, mailURL(fid, ""))
			return
		} else if ok {
			m.Draft = true
		}
		if m.Draft { // a draft opens in the editor, as in Outlook
			data["New"], data["Letter"] = true, nil
			q = url.Values{"draft": {m.ID}, "folder": {fid}}
		}
		for i := range boxes {
			if boxes[i].ID == m.MailboxID {
				box = &boxes[i]
			}
		}
	}
	if data["New"] == true && len(boxes) > 0 {
		cq := url.Values{}
		for k, v := range q {
			cq[k] = v
		}
		cq.Set("folder", fid)
		c, err := s.mailCompose(r, sess, cq)
		if err != nil {
			s.taskErr(w, r, err, mailURL(fid, ""))
			return
		}
		data["Compose"] = c
	}
	data["Box"] = box
	if data["Shares"], err = s.Mail.Shares(ctx, sess.UserID); err != nil { // the shared mailboxes (mailshared.go)
		s.Log.Warn("mail shares", "err", err)
	}
	if len(boxes) > 0 { // the banner while the automatic replies go, as Outlook's
		if _, st, err := s.Mail.OOONow(ctx, sess.UserID, time.Now()); err != nil {
			s.Log.Warn("mail automatic replies", "err", err)
		} else if st.On {
			data["OOO"] = st
		}
	}
	s.render(w, r, "mail", page{TitleKey: "mail.title", Nav: "mail", Data: data})
}

// mailRowFiles are the files each row of the list shows under its text («Макет»: the names of the letter's files),
// those of all the letters of a conversation.
func (s *Server) mailRowFiles(ctx context.Context, userID string, list []mail.Message) (map[string][]mail.File, error) {
	var ids []string
	for _, m := range list {
		if m.Attached > 0 {
			ids = append(ids, m.IDs...)
		}
	}
	files, err := s.Mail.FilesOf(ctx, userID, ids)
	if err != nil {
		return nil, err
	}
	out := map[string][]mail.File{}
	for _, m := range list {
		for _, id := range m.IDs {
			out[m.ID] = append(out[m.ID], files[id]...)
		}
	}
	return out, nil
}

// mailRead is what the reading pane shows of a letter: its conversation (all read now; the letter itself and the
// unread ones open, the rest folded), the files of each, the folders the letter may go to.
func (s *Server) mailRead(r *http.Request, sess auth.Session, id string) (map[string]any, error) {
	h, err := s.Mail.Handling(r.Context(), sess.UserID)
	if err != nil {
		return nil, err
	}
	thread, err := s.Mail.Thread(r.Context(), sess.UserID, id, h.ReadAfter == 0)
	if err != nil {
		return nil, err
	}
	var m mail.Message
	var unread []string // the script marks them read later («Обработка сообщений»)
	files := map[string][]mail.File{}
	for _, t := range thread {
		if t.ID == id {
			m = t
		}
		if !t.Seen {
			unread = append(unread, t.ID)
		}
		if files[t.ID], err = s.Mail.Files(r.Context(), sess.UserID, t.ID); err != nil {
			return nil, err
		}
	}
	folders, err := s.Mail.Folders(r.Context(), sess.UserID, m.MailboxID)
	if err != nil {
		return nil, err
	}
	var to []mail.Folder
	for _, f := range folders {
		if f.Selectable && f.ID != m.FolderID {
			to = append(to, f)
		}
	}
	cats, err := s.mailCategories(r, sess.UserID)
	if err != nil {
		return nil, err
	}
	act := []string{m.ID} // shown as conversations, the actions take the conversation's letters of the folder
	if mailConv(r) {
		act = act[:0]
		for _, t := range thread {
			if t.FolderID == m.FolderID {
				act = append(act, t.ID)
			}
		}
	}
	docs := map[string]string{} // the HTML letters, each a page of its own in a sandboxed frame
	outside := false
	for _, t := range thread {
		if t.HTML == "" {
			continue
		}
		pics, err := s.Mail.Pictures(r.Context(), sess.UserID, t.ID)
		if err != nil {
			return nil, err
		}
		h := mail.WithPictures(t.HTML, pics)
		if kind, _ := s.Mail.SenderKind(r.Context(), sess.UserID, t.FromAddr); kind == mail.SenderSafe { // «Надёжные отправители»
			docs[t.ID] = strings.Replace(mailDoc(h), mailCSP, mailCSPOpen, 1)
			continue
		}
		docs[t.ID] = mailDoc(h)
		outside = outside || mail.OutsidePictures(h)
	}
	prefs, err := s.Mail.Prefs(r.Context(), sess.UserID)
	if err != nil {
		return nil, err
	}
	ignored, err := s.Mail.Ignored(r.Context(), sess.UserID, m.ID)
	if err != nil {
		return nil, err
	}
	quick, err := s.mailQuickSteps(r, sess.UserID)
	if err != nil {
		return nil, err
	}
	if m.ReceiptTo != "" && !m.ReceiptDone && h.Receipts != "ask" { // a read receipt always sent, or never
		rc, decline := s.mailReceiptOf(r, m), h.Receipts == "never"
		m.ReceiptDone = true
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := s.Mail.SendReceipt(ctx, sess.UserID, m.ID, rc, decline); err != nil {
				s.Log.Warn("mail read receipt", "letter", m.ID, "err", err)
			}
		}()
	}
	unsub, unsubPage := "", "" // «Отказаться от подписки»
	if m.Unsubscribe && m.Unsubscribed == nil && !m.Outgoing() {
		if unsub, unsubPage, err = s.Mail.UnsubscribeWay(r.Context(), sess.UserID, m.ID); err != nil {
			return nil, err
		}
	}
	var votes mail.Tracking // the voting buttons: the user's vote, or «Отслеживание» of the user's own letter
	if m.Voting != "" {
		if votes, err = s.Mail.Votes(r.Context(), sess.UserID, m.ID); err != nil {
			return nil, err
		}
	}
	var recalls []mail.RecallResult // «Отозвать сообщение» of the user's sent letter
	if m.FolderRole == "sent" {
		if recalls, err = s.Mail.Recalls(r.Context(), sess.UserID, m.MessageID); err != nil {
			return nil, err
		}
	}
	data := map[string]any{"Letter": m, "Ignored": ignored, "Votes": votes, "Recalls": recalls, "QuickSteps": quick, "Thread": thread, "Files": files, "MoveTo": to, "Categories": cats, "CatByID": catMap(cats), "ActIDs": act,
		"SnoozePresets": snoozePresets, "FlagPresets": flagPresets, "RemindPresets": remindPresets, "Docs": docs, "Outside": outside,
		"Unsub": unsub, "UnsubPage": unsubPage, "Focus": prefs.Focused && m.FolderRole == "inbox",
		"ReadAfter": h.ReadAfter, "Unread": unread}
	if err := s.mailExtras(r, sess, m, thread, data); err != nil { // its meeting, tasks, CRM, the sender (maillinks.go)
		return nil, err
	}
	return data, nil
}

// mailCSP is the policy of a letter's frame: no scripts, nothing loaded but go24's own pictures (the letter's) —
// pictures from elsewhere wait till the user asks (the script puts mailCSPOpen in).
const (
	mailCSP     = "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; font-src data:"
	mailCSPOpen = "default-src 'none'; img-src * data:; style-src 'unsafe-inline'; font-src data:"
)

// mailDoc is the page of an HTML letter for its frame: links open in a new tab.
func mailDoc(h string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="` + mailCSP + `">` +
		`<base target="_blank"><style>html,body{margin:0;background:#fff}body{font:14px/1.45 system-ui,-apple-system,"Segoe UI",sans-serif;` +
		`color:#1f2328;overflow-wrap:anywhere;padding:2px}img{max-width:100%;height:auto}table{max-width:100%}` +
		`blockquote{margin:0 0 0 .8em;padding-left:.8em;border-left:2px solid #c8ccd1}</style></head><body>` + h + `</body></html>`
}

func catMap(list []mail.Category) map[string]mail.Category {
	out := make(map[string]mail.Category, len(list))
	for _, c := range list {
		out[c.ID] = c
	}
	return out
}

// mailLetter is the reading pane of a letter for the script (the list stays as it is); a draft — the editor.
func (s *Server) mailLetter(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	data, err := s.mailRead(r, sess, r.PathValue("letter"))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	data["FolderID"] = r.URL.Query().Get("folder")
	var buf bytes.Buffer
	m := data["Letter"].(mail.Message)
	if ok, err := s.Mail.AdoptDraft(r.Context(), sess.UserID, m.ID); err != nil {
		s.jsonErr(w, r, err)
		return
	} else if ok {
		m.Draft = true
	}
	if m.Draft {
		c, err := s.mailCompose(r, sess, url.Values{"draft": {m.ID}, "folder": {r.URL.Query().Get("folder")}})
		if err != nil {
			s.jsonErr(w, r, err)
			return
		}
		err = s.pages[s.lang(r)]["mail"].ExecuteTemplate(&buf, "mcompose", c)
		if err != nil {
			s.jsonErr(w, r, err)
			return
		}
	} else if err := s.pages[s.lang(r)]["mail"].ExecuteTemplate(&buf, "mailread", data); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

// mailMove moves letters to a folder (drag and drop, «Переместить»).
func (s *Server) mailMove(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.mailErr(w, r, i18n.E("err.bad_form"), "/mail")
		return
	}
	ids, folder, back := letterIDs(r.Form["letter"]), r.FormValue("folder"), r.FormValue("back")
	if !strings.HasPrefix(back, "/mail") {
		back = "/mail"
	}
	back = withLetter(back, r.FormValue("next"))
	if err := s.Mail.Move(r.Context(), sess.UserID, ids, folder); err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	s.Log.Info("mail moved", "user_id", sess.UserID, "letters", len(ids), "folder", folder)
	if r.Header.Get("Accept") == "application/json" {
		writeJSON(w, http.StatusOK, map[string]any{"result": true})
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) mailFolderAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	box := r.PathValue("id")
	id, err := s.Mail.CreateFolder(r.Context(), sess.UserID, box, r.FormValue("parent"), r.FormValue("name"))
	if err != nil {
		s.mailErr(w, r, err, mailURL(r.FormValue("back_folder"), ""))
		return
	}
	s.Log.Info("mail folder created", "user_id", sess.UserID, "mailbox", box, "folder", id)
	http.Redirect(w, r, mailURL(id, ""), http.StatusSeeOther)
}

func (s *Server) mailFolderRename(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if err := s.Mail.RenameFolder(r.Context(), sess.UserID, id, r.FormValue("name")); err != nil {
		s.mailErr(w, r, err, mailURL(id, ""))
		return
	}
	http.Redirect(w, r, mailURL(id, ""), http.StatusSeeOther)
}

func (s *Server) mailFolderDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if err := s.Mail.DeleteFolder(r.Context(), sess.UserID, id); err != nil {
		s.mailErr(w, r, err, mailURL(id, ""))
		return
	}
	s.Log.Info("mail folder deleted", "user_id", sess.UserID, "folder", id)
	http.Redirect(w, r, "/mail", http.StatusSeeOther)
}

func (s *Server) mailAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ip, _ := strconv.Atoi(r.FormValue("imap_port"))
	sp, _ := strconv.Atoi(r.FormValue("smtp_port"))
	st := mail.Settings{Email: r.FormValue("email"), IMAPHost: r.FormValue("imap_host"), IMAPPort: ip, SMTPHost: r.FormValue("smtp_host"),
		SMTPPort: sp, Login: r.FormValue("login"), Password: r.FormValue("password"), TLS: r.FormValue("tls") != "off"}
	err := mail.Probe(st) // the mailbox is kept only if go24 can sign in to it
	var id string
	if err == nil {
		id, err = s.Mail.Add(r.Context(), sess.UserID, st)
	}
	if err != nil { // why a mailbox was not connected (never the password)
		s.Log.Warn("mailbox not connected", "user_id", sess.UserID, "imap", st.IMAPHost, "imap_port", st.IMAPPort,
			"smtp", st.SMTPHost, "smtp_port", st.SMTPPort, "tls", st.TLS, "err", err)
		http.Redirect(w, r, "/mail?msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	if err := s.Mail.Sync(r.Context(), id); err != nil {
		s.Log.Warn("first mail sync", "err", err)
	}
	http.Redirect(w, r, "/mail?box="+id, http.StatusSeeOther)
}

func (s *Server) mailRemove(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Mail.Remove(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/mail")
		return
	}
	http.Redirect(w, r, "/mail", http.StatusSeeOther)
}

// mailSync fetches the box now («Обновить»).
func (s *Server) mailSync(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if _, err := s.Mail.Mailbox(r.Context(), sess.UserID, id); err != nil {
		s.taskErr(w, r, err, "/mail")
		return
	}
	back := "/mail?box=" + url.QueryEscape(id)
	if err := s.Mail.Sync(r.Context(), id); err != nil {
		back += "&msg=" + url.QueryEscape(s.errText(r, err))
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) mailSend(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	back := "/mail?box=" + url.QueryEscape(id)
	r.Body = http.MaxBytesReader(w, r.Body, mail.MaxSendSize+1<<20)
	var files []mail.Attachment
	if err := r.ParseMultipartForm(8 << 20); err == nil && r.MultipartForm != nil {
		for _, h := range r.MultipartForm.File["files"] {
			f, err := h.Open()
			if err != nil {
				continue
			}
			data, err := io.ReadAll(io.LimitReader(f, mail.MaxSendSize+1))
			f.Close()
			if err != nil {
				continue
			}
			ct := h.Header.Get("Content-Type")
			if ct == "" {
				ct = "application/octet-stream"
			}
			files = append(files, mail.Attachment{Name: path.Base(strings.ReplaceAll(h.Filename, "\\", "/")), ContentType: ct, Data: data})
		}
	} else if err != nil && !errors.Is(err, http.ErrNotMultipart) {
		http.Redirect(w, r, back+"&new=1&msg="+url.QueryEscape(s.tr(r, "err.mail_files", mail.MaxSendFiles)), http.StatusSeeOther)
		return
	}
	refs, err := s.Mail.Refs(r.Context(), sess.UserID, r.FormValue("reply_to"))
	if err == nil {
		imp, _ := strconv.Atoi(r.FormValue("importance"))
		err = s.Mail.SendLetter(r.Context(), sess.UserID, id, mail.Letter{From: r.FormValue("from"), To: r.FormValue("to"), CC: r.FormValue("cc"), BCC: r.FormValue("bcc"),
			Subject: r.FormValue("subject"), Text: r.FormValue("body"), HTML: r.FormValue("html"), Files: files, Importance: max(-1, min(1, imp)),
			Receipt: r.FormValue("receipt") != "", Delivery: r.FormValue("delivery") != "", Refs: refs})
	}
	if err != nil {
		http.Redirect(w, r, back+"&new=1&msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back+"&msg="+url.QueryEscape(s.tr(r, "mail.sent_ok")), http.StatusSeeOther)
}

// mailLead makes a CRM lead of a letter: the subject, the sender's name and e-mail, the text as the comment.
func (s *Server) mailLead(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	m, err := s.Mail.Message(r.Context(), sess.UserID, r.PathValue("letter"))
	if err != nil {
		s.taskErr(w, r, err, "/mail")
		return
	}
	if m.LeadID != "" {
		http.Redirect(w, r, "/crm/leads/"+m.LeadID, http.StatusSeeOther)
		return
	}
	if err := s.CRM.Check(r.Context(), sess.UserID, "lead", "write", sess.UserID); err != nil {
		s.mailErr(w, r, err, mailURL(m.FolderID, m.ID))
		return
	}
	title := m.Subject
	if title == "" {
		title = m.From()
	}
	body := m.Body
	if r := []rune(body); len(r) > 9000 {
		body = string(r[:9000])
	}
	lead, err := s.CRM.SaveLead(r.Context(), sess.UserID, crm.Lead{Title: title, Name: m.FromName, Email: m.FromAddr,
		Origin: crm.Dict{Code: "EMAIL"}, Comment: body})
	if err == nil {
		err = s.Mail.SetLead(r.Context(), m.ID, lead)
	}
	if err == nil { // the letter is in the lead's timeline
		_, err = s.Mail.LinkCRM(r.Context(), sess.UserID, m.ID, "lead", lead)
	}
	if err != nil {
		s.mailErr(w, r, err, mailURL(m.FolderID, m.ID))
		return
	}
	http.Redirect(w, r, "/crm/leads/"+lead, http.StatusSeeOther)
}

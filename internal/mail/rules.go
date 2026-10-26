package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Rules, as Outlook's «Правила»: conditions (all of them must hold; none — every letter), exceptions (any of them
// keeps the rule off the letter), actions; run in their order on each new letter of the inbox, or on a folder at once
// («Запустить сейчас»). «Прекратить обработку других правил» stops at the rule. A letter gets what all its rules
// give; the first rule that deletes, marks as junk or moves it decides where it goes (that is done last). Letters
// forwarded and redirected by a rule carry Auto-Submitted: auto-forwarded — nobody answers them automatically.

// Cond is what a rule looks for in a letter. Words and addresses are comma-separated; any of them will do.
type Cond struct {
	From       string `json:"from,omitempty"`       // the sender's address or name has one
	To         string `json:"to,omitempty"`         // «Кому» or «Копия» has one
	Subject    string `json:"subject,omitempty"`    // the subject has one
	Body       string `json:"body,omitempty"`       // the text has one
	Attach     bool   `json:"attach,omitempty"`     // the letter has files
	Importance string `json:"importance,omitempty"` // high, normal, low
	ToMe       bool   `json:"to_me,omitempty"`      // the mailbox is in «Кому»
	CcMe       bool   `json:"cc_me,omitempty"`      // the mailbox is in «Копия»
	OnlyMe     bool   `json:"only_me,omitempty"`    // sent to the mailbox only
}

// Do is what a rule does with a letter.
type Do struct {
	Move          string `json:"move,omitempty"`     // a folder's id (letters of its mailbox only)
	Category      string `json:"category,omitempty"` // a category's id
	Importance    string `json:"importance,omitempty"`
	Flag          bool   `json:"flag,omitempty"`
	Pin           bool   `json:"pin,omitempty"`
	Read          bool   `json:"read,omitempty"`
	Delete        bool   `json:"delete,omitempty"`
	Junk          bool   `json:"junk,omitempty"`
	Forward       string `json:"forward,omitempty"`        // addresses
	ForwardAttach string `json:"forward_attach,omitempty"` // addresses: the letter goes as an attachment
	Redirect      string `json:"redirect,omitempty"`       // addresses: the letter goes as it is, answers to its sender
	Notify        bool   `json:"notify,omitempty"`         // «Уведомить»: the letter pops up wherever it goes (see announce)
}

// Rule is a rule of the user.
type Rule struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Stop    bool   `json:"stop"` // «Прекратить обработку других правил»
	Pos     int    `json:"pos"`
	If      Cond   `json:"if"`
	Except  Cond   `json:"except"`
	Do      Do     `json:"do"`
}

// Importances a rule looks for and sets.
var Importances = []string{"high", "normal", "low"}

func importanceName(i int) string {
	switch i {
	case 1:
		return "high"
	case -1:
		return "low"
	}
	return "normal"
}

// facts are what the rules look at in a letter.
type facts struct {
	id, mailbox, folder                   string
	from, name, to, cc, subject, body, me string
	attach                                bool
	importance                            int
}

// terms are the words of a list, lower case.
func terms(list string) []string {
	var out []string
	for _, t := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// hasAny: does the text have one of the words?
func hasAny(text, list string) bool {
	text = strings.ToLower(text)
	for _, t := range terms(list) {
		if strings.Contains(text, t) {
			return true
		}
	}
	return false
}

// addrs are the addresses of a list as go24 keeps them (lower case).
func addrs(list string) []string {
	var out []string
	for _, a := range terms(list) {
		if i, j := strings.LastIndex(a, "<"), strings.LastIndex(a, ">"); i >= 0 && j > i {
			a = a[i+1 : j]
		}
		out = append(out, a)
	}
	return out
}

func inAddrs(list, addr string) bool {
	for _, a := range addrs(list) {
		if a == strings.ToLower(addr) {
			return true
		}
	}
	return false
}

// tests are the results of the conditions that are set.
func (c Cond) tests(f facts) []bool {
	var out []bool
	if strings.TrimSpace(c.From) != "" {
		out = append(out, hasAny(f.from+" "+f.name, c.From))
	}
	if strings.TrimSpace(c.To) != "" {
		out = append(out, hasAny(f.to+" "+f.cc, c.To))
	}
	if strings.TrimSpace(c.Subject) != "" {
		out = append(out, hasAny(f.subject, c.Subject))
	}
	if strings.TrimSpace(c.Body) != "" {
		out = append(out, hasAny(f.body, c.Body))
	}
	if c.Attach {
		out = append(out, f.attach)
	}
	if c.Importance != "" {
		out = append(out, importanceName(f.importance) == c.Importance)
	}
	if c.ToMe {
		out = append(out, inAddrs(f.to, f.me))
	}
	if c.CcMe {
		out = append(out, inAddrs(f.cc, f.me))
	}
	if c.OnlyMe {
		all := addrs(f.to + "," + f.cc)
		out = append(out, len(all) == 1 && all[0] == strings.ToLower(f.me))
	}
	return out
}

// Empty: nothing is set.
func (c Cond) Empty() bool { return len(c.tests(facts{})) == 0 }

// Matches: does the rule take the letter — all its conditions hold and none of its exceptions?
func (r Rule) matches(f facts) bool {
	for _, ok := range r.If.tests(f) {
		if !ok {
			return false
		}
	}
	for _, ok := range r.Except.tests(f) {
		if ok {
			return false
		}
	}
	return true
}

// Empty: the rule does nothing.
func (d Do) Empty() bool {
	return d.Move == "" && d.Category == "" && d.Importance == "" && !d.Flag && !d.Pin && !d.Read && !d.Delete && !d.Junk && !d.Notify &&
		strings.TrimSpace(d.Forward+d.ForwardAttach+d.Redirect) == ""
}

// MaxRules bounds the user's rules.
const MaxRules = 200

// checkRule: the name, an action at least, the addresses, the user's own folder and category.
func (s *Store) checkRule(ctx context.Context, userID string, r *Rule) error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" || utf8.RuneCountInString(r.Name) > 200 {
		return i18n.E("err.mail_rule_name")
	}
	if r.Do.Empty() {
		return i18n.E("err.mail_rule_do")
	}
	for _, c := range []*Cond{&r.If, &r.Except} {
		if c.Importance != "" && !oneOf(c.Importance, Importances) {
			return i18n.E("err.bad_form")
		}
	}
	if r.Do.Importance != "" && !oneOf(r.Do.Importance, Importances) {
		return i18n.E("err.bad_form")
	}
	for _, list := range []*string{&r.Do.Forward, &r.Do.ForwardAttach, &r.Do.Redirect} {
		a, err := addresses(*list)
		if err != nil || len(a) > 20 {
			return i18n.E("err.mail_to")
		}
		*list = joinAddrs(a)
	}
	if r.Do.Move != "" {
		f, err := s.Folder(ctx, userID, r.Do.Move)
		if err != nil {
			return err
		}
		if !f.Selectable {
			return i18n.E("err.mail_folder_noselect")
		}
	}
	if r.Do.Category != "" {
		if _, err := s.Category(ctx, userID, r.Do.Category); err != nil {
			return err
		}
	}
	return nil
}

const ruleCols = `id::text, name, enabled, stop, pos, cond, exc, act`

func scanRules(rows pgx.Rows, err error) ([]Rule, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		var r Rule
		var c, e, d []byte
		if err := rows.Scan(&r.ID, &r.Name, &r.Enabled, &r.Stop, &r.Pos, &c, &e, &d); err != nil {
			return nil, err
		}
		if err := errors.Join(json.Unmarshal(c, &r.If), json.Unmarshal(e, &r.Except), json.Unmarshal(d, &r.Do)); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Rules are the user's rules in their order.
func (s *Store) Rules(ctx context.Context, userID string) ([]Rule, error) {
	return scanRules(s.db.Query(ctx, `SELECT `+ruleCols+` FROM mail_rules WHERE user_id::text = $1 ORDER BY pos, created_at`, userID))
}

// Rule is a rule of the user.
func (s *Store) Rule(ctx context.Context, userID, id string) (Rule, error) {
	list, err := scanRules(s.db.Query(ctx, `SELECT `+ruleCols+` FROM mail_rules WHERE id::text = $1 AND user_id::text = $2`, id, userID))
	if err != nil {
		return Rule{}, err
	}
	if len(list) == 0 {
		return Rule{}, ErrNotFound
	}
	return list[0], nil
}

// SaveRule makes a rule of the user (r.ID "": the last one) or changes one; its id.
func (s *Store) SaveRule(ctx context.Context, userID string, r Rule) (string, error) {
	if err := s.checkRule(ctx, userID, &r); err != nil {
		return "", err
	}
	c, _ := json.Marshal(r.If)
	e, _ := json.Marshal(r.Except)
	d, _ := json.Marshal(r.Do)
	if r.ID == "" {
		var n int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM mail_rules WHERE user_id::text = $1`, userID).Scan(&n); err != nil {
			return "", err
		}
		if n >= MaxRules {
			return "", i18n.E("err.mail_rules_many", MaxRules)
		}
		err := s.db.QueryRow(ctx, `INSERT INTO mail_rules (user_id, name, enabled, stop, cond, exc, act, pos)
			VALUES ($1, $2, $3, $4, $5, $6, $7, (SELECT coalesce(max(pos), 0) + 1 FROM mail_rules WHERE user_id = $1)) RETURNING id::text`,
			userID, r.Name, r.Enabled, r.Stop, c, e, d).Scan(&r.ID)
		return r.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE mail_rules SET name = $3, enabled = $4, stop = $5, cond = $6, exc = $7, act = $8
		WHERE id::text = $1 AND user_id::text = $2`, r.ID, userID, r.Name, r.Enabled, r.Stop, c, e, d)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return r.ID, err
}

// DeleteRule deletes a rule of the user.
func (s *Store) DeleteRule(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM mail_rules WHERE id::text = $1 AND user_id::text = $2`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// EnableRule turns a rule of the user on or off.
func (s *Store) EnableRule(ctx context.Context, userID, id string, on bool) error {
	tag, err := s.db.Exec(ctx, `UPDATE mail_rules SET enabled = $3 WHERE id::text = $1 AND user_id::text = $2`, id, userID, on)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// MoveRule moves a rule of the user up (d < 0) or down in the order.
func (s *Store) MoveRule(ctx context.Context, userID, id string, d int) error {
	list, err := s.Rules(ctx, userID)
	if err != nil {
		return err
	}
	at := -1
	for i, r := range list {
		if r.ID == id {
			at = i
		}
	}
	if at < 0 {
		return ErrNotFound
	}
	to := at + 1
	if d < 0 {
		to = at - 1
	}
	if to < 0 || to >= len(list) {
		return nil
	}
	list[at], list[to] = list[to], list[at]
	for i, r := range list {
		if _, err := s.db.Exec(ctx, `UPDATE mail_rules SET pos = $2 WHERE id::text = $1`, r.ID, i+1); err != nil {
			return err
		}
	}
	return nil
}

// RunRules runs rules of the user on the letters of a folder now («Запустить сейчас»): the rules of ruleIDs (on or
// off), or all that are on; the number of letters the rules took.
func (s *Store) RunRules(ctx context.Context, userID, folderID string, ruleIDs []string) (int, error) {
	f, err := s.Folder(ctx, userID, folderID)
	if err != nil {
		return 0, err
	}
	all, err := s.Rules(ctx, userID)
	if err != nil {
		return 0, err
	}
	pick := map[string]bool{}
	for _, id := range ruleIDs {
		pick[id] = true
	}
	var rules []Rule
	for _, r := range all {
		if (len(pick) == 0 && r.Enabled) || pick[r.ID] {
			rules = append(rules, r)
		}
	}
	if len(rules) == 0 {
		return 0, i18n.E("err.mail_rules_none")
	}
	rows, err := s.db.Query(ctx, `SELECT x.id::text FROM mail_messages x LEFT JOIN mail_drafts d ON d.id = x.id
		WHERE x.folder_id::text = $1 AND d.id IS NULL ORDER BY x.sent_at DESC LIMIT 5000`, f.ID)
	if err != nil {
		return 0, err
	}
	ids, err := pgxCollectStrings(rows)
	if err != nil {
		return 0, err
	}
	return s.applyRules(ctx, userID, rules, ids)
}

// plan is what the rules do with a letter.
type plan struct {
	read, flag, pin bool
	importance      string
	cats            []string
	sends           [][2]string // how (forward, attach, redirect), to whom
	final           string      // delete, junk, move:<folder> ("" — the letter stays)
}

// applyRules runs the rules on the letters; the number of letters the rules took. A failure does not stop the rest.
func (s *Store) applyRules(ctx context.Context, userID string, rules []Rule, letterIDs []string) (int, error) {
	if len(rules) == 0 || len(letterIDs) == 0 {
		return 0, nil
	}
	rows, err := s.db.Query(ctx, `SELECT x.id::text, x.mailbox_id::text, x.folder_id::text, x.from_addr, x.from_name, x.to_addr, x.cc_addr,
			x.subject, x.body, m.email, EXISTS (SELECT 1 FROM files fl WHERE fl.mail_id = x.id AND fl.content_id = ''), x.importance::int
		FROM mail_messages x JOIN mailboxes m ON m.id = x.mailbox_id WHERE x.id::text = ANY($1) AND m.user_id::text = $2`, letterIDs, userID)
	if err != nil {
		return 0, err
	}
	var list []facts
	for rows.Next() {
		var f facts
		if err := rows.Scan(&f.id, &f.mailbox, &f.folder, &f.from, &f.name, &f.to, &f.cc, &f.subject, &f.body, &f.me, &f.attach, &f.importance); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	plans := map[string]*plan{}
	for _, f := range list {
		var p plan
		took := false
		for _, r := range rules {
			if !r.matches(f) {
				continue
			}
			took = true
			d := r.Do
			p.read, p.flag, p.pin = p.read || d.Read, p.flag || d.Flag, p.pin || d.Pin
			if d.Importance != "" {
				p.importance = d.Importance
			}
			if d.Category != "" {
				p.cats = append(p.cats, d.Category)
			}
			for _, x := range [][2]string{{"forward", d.Forward}, {"attach", d.ForwardAttach}, {"redirect", d.Redirect}} {
				if x[1] != "" {
					p.sends = append(p.sends, x)
				}
			}
			if p.final == "" {
				switch {
				case d.Delete:
					p.final = "delete"
				case d.Junk:
					p.final = "junk"
				case d.Move != "" && d.Move != f.folder:
					p.final = "move:" + d.Move
				}
			}
			if r.Stop {
				break
			}
		}
		if took {
			plans[f.id] = &p
		}
	}
	var errs []error
	group := func(want func(*plan) string) map[string][]string {
		out := map[string][]string{}
		for id, p := range plans {
			if k := want(p); k != "" {
				out[k] = append(out[k], id)
			}
		}
		return out
	}
	for cat, ids := range group(func(p *plan) string { return strings.Join(p.cats, ",") }) {
		for _, c := range strings.Split(cat, ",") { // given, never taken off (Act toggles)
			if _, err := s.db.Exec(ctx, `UPDATE mail_messages SET categories = categories || $2::uuid WHERE id::text = ANY($1) AND NOT categories @> ARRAY[$2::uuid]`,
				ids, c); err != nil {
				errs = append(errs, err)
			}
		}
	}
	for imp, ids := range group(func(p *plan) string { return p.importance }) {
		v := map[string]int{"high": 1, "normal": 0, "low": -1}[imp]
		if _, err := s.db.Exec(ctx, `UPDATE mail_messages SET importance = $2 WHERE id::text = ANY($1)`, ids, v); err != nil {
			errs = append(errs, err)
		}
	}
	for _, a := range []struct {
		action string
		on     func(*plan) bool
	}{{"read", func(p *plan) bool { return p.read }}, {"flag", func(p *plan) bool { return p.flag }}, {"pin", func(p *plan) bool { return p.pin }}} {
		var ids []string
		for id, p := range plans {
			if a.on(p) {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			errs = append(errs, s.Act(ctx, userID, ids, a.action, ""))
		}
	}
	for id, p := range plans {
		for _, x := range p.sends {
			errs = append(errs, s.ruleSend(ctx, userID, id, x[0], x[1]))
		}
	}
	for final, ids := range group(func(p *plan) string { return p.final }) {
		action, folder, _ := strings.Cut(final, ":")
		if action == "move" { // a rule's folder takes the letters of its own mailbox
			f, err := s.Folder(ctx, userID, folder)
			if err != nil {
				continue // the folder is gone
			}
			var same []string
			for _, l := range list {
				if l.mailbox == f.MailboxID && contains(ids, l.id) {
					same = append(same, l.id)
				}
			}
			if len(same) > 0 {
				errs = append(errs, s.Move(ctx, userID, same, folder))
			}
			continue
		}
		errs = append(errs, s.Act(ctx, userID, ids, action, ""))
	}
	return len(plans), errors.Join(errs...)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// tr is a text in go24's language (Tr), or its key.
func (s *Store) tr(key string, args ...any) string {
	if s.Tr == nil {
		return key
	}
	return s.Tr(key, args...)
}

// letterFiles are all the files of a letter of the user (its pictures too), read back.
func (s *Store) letterFiles(ctx context.Context, userID, letterID string) ([]Attachment, error) {
	if s.Open == nil {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `SELECT f.id::text FROM files f JOIN mail_messages x ON x.id = f.mail_id JOIN mailboxes m ON m.id = x.mailbox_id
		WHERE x.id::text = $1 AND m.user_id::text = $2 ORDER BY f.name`, letterID, userID)
	if err != nil {
		return nil, err
	}
	ids, err := pgxCollectStrings(rows)
	if err != nil {
		return nil, err
	}
	var out []Attachment
	for _, id := range ids {
		f, err := s.readFile(ctx, userID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// quoteHTML is the letter under a forward, as Outlook quotes it: who it was from, when, to whom, its subject.
func (s *Store) quoteHTML(m Message) string {
	e := html.EscapeString
	var b strings.Builder
	b.WriteString(`<hr style="display:inline-block;width:98%"><div><b>` + e(s.tr("mail.from")) + `:</b> ` + e(m.From()))
	if m.FromName != "" {
		b.WriteString(" &lt;" + e(m.FromAddr) + "&gt;")
	}
	b.WriteString(`<br><b>` + e(s.tr("mail.sent_at")) + `:</b> ` + e(m.SentAt.In(time.Local).Format("02.01.2006 15:04")))
	b.WriteString(`<br><b>` + e(s.tr("mail.to")) + `:</b> ` + e(m.To))
	if m.CC != "" {
		b.WriteString(`<br><b>` + e(s.tr("mail.cc")) + `:</b> ` + e(m.CC))
	}
	b.WriteString(`<br><b>` + e(s.tr("mail.subject")) + `:</b> ` + e(m.Subject) + `</div><br>`)
	if m.HTML != "" {
		b.WriteString("<div>" + m.HTML + "</div>")
	} else {
		b.WriteString("<div>" + TextHTML(m.Body) + "</div>")
	}
	return b.String()
}

// fwdSubject is a forward's subject, as Outlook's.
func fwdSubject(subject string) string {
	if low := strings.ToLower(subject); strings.HasPrefix(low, "fw:") || strings.HasPrefix(low, "fwd:") {
		return subject
	}
	return "FW: " + subject
}

// letterAttachment is a letter with its files as one attachment («Переслать как вложение»): «Тема.eml».
func letterAttachment(m Message, files []Attachment) Attachment {
	e := envelope{From: mail.Address{Name: m.FromName, Address: m.FromAddr}, Now: m.SentAt, ID: m.MessageID,
		Host: m.FromAddr[strings.LastIndex(m.FromAddr, "@")+1:]}
	e.To, _ = addresses(m.To)
	e.CC, _ = addresses(m.CC)
	raw, _ := compose(e, Letter{Subject: m.Subject, Text: m.Body, HTML: m.HTML, Files: files, Importance: m.Importance})
	name := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`\/:*?"<>|`, r) {
			return '_'
		}
		return r
	}, clip(m.Subject, 100))
	if name == "" {
		name = "message"
	}
	return Attachment{Name: name + ".eml", ContentType: "message/rfc822", Data: raw}
}

// ruleSend sends a letter of the user on by a rule: forward (with its files), attach (the letter as an attachment),
// redirect (as it is, from the user's mailbox in its sender's name, answers going to its sender; no copy kept).
func (s *Store) ruleSend(ctx context.Context, userID, letterID, how, to string) error {
	return s.sendOn(ctx, userID, letterID, how, to, "auto-forwarded")
}

// sendOn sends a letter of the user on (see ruleSend); auto: its Auto-Submitted ("" — the user's own, a quick step's).
func (s *Store) sendOn(ctx context.Context, userID, letterID, how, to, auto string) error {
	m, err := s.message(ctx, userID, letterID)
	if err != nil {
		return err
	}
	files, err := s.letterFiles(ctx, userID, letterID)
	if err != nil {
		return err
	}
	l := Letter{To: to, auto: auto}
	switch how {
	case "forward":
		l.Subject, l.HTML, l.Files = fwdSubject(m.Subject), s.quoteHTML(m), files
	case "attach":
		l.Subject, l.Text = fwdSubject(m.Subject), " "
		l.Files = []Attachment{letterAttachment(m, files)}
	case "redirect":
		l.Subject, l.Text, l.HTML, l.Files, l.Importance = m.Subject, m.Body, m.HTML, files, m.Importance
		l.fromName, l.replyTo, l.noCopy = m.From(), &mail.Address{Name: m.FromName, Address: m.FromAddr}, true
		l.resentFrom = &mail.Address{Address: m.MailboxEmail}
	default:
		return fmt.Errorf("mail: rule send %q", how)
	}
	return s.SendLetter(ctx, userID, m.MailboxID, l)
}

// enabledRules are the user's rules that are on, in their order.
func (s *Store) enabledRules(ctx context.Context, userID string) ([]Rule, error) {
	all, err := s.Rules(ctx, userID)
	var on []Rule
	for _, r := range all {
		if r.Enabled {
			on = append(on, r)
		}
	}
	return on, err
}

// arrive: the letters new in a mailbox's inbox get the automatic reply, are forwarded by its «Пересылка», then go
// through the user's rules, as they do in Outlook on arrival (failures are logged; the standing «Очистка» is the
// worker's: SweepDue); the letters left in the mailbox (those forwarded without a copy are not).
func (s *Store) arrive(ctx context.Context, userID, mailboxID string, news []arrived) []arrived {
	logf := func(what string, err error) {
		if err != nil && s.Log != nil {
			s.Log(what, mailboxID, err)
		}
	}
	logf("mail: automatic reply", s.autoReply(ctx, userID, mailboxID, news))
	news, err := s.forward(ctx, userID, mailboxID, news)
	logf("mail: forwarding", err)
	rules, err := s.enabledRules(ctx, userID)
	if err == nil && len(rules) > 0 && len(news) > 0 {
		ids := make([]string, len(news))
		for i, n := range news {
			ids[i] = n.id
		}
		_, err = s.applyRules(ctx, userID, rules, ids)
	}
	logf("mail: rules", err)
	return news
}

package telephony

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Внешние линии» SIP connector: the provider's SIP trunk. Asterisk registers at the provider's server
// with the login and password (Register) and so gets the calls to the line's number; they ring the employees of Ring —
// all at once or by turns (RingMode "queue", RingTurn seconds each). An employee dials a number longer than an inner
// one: the call goes out through the first line (by Position) they may call by — anyone's (OutAll) or Out's — with the
// line's number shown. As the number settings, a line may have working hours (out of them the call rings
// nobody) and says what happens to a call nobody answered and to one out of the working hours: hang up, voicemail
// (the line's greeting, a beep and the client's message — in the log with its record) or forward to a number.

// RingTurn is how long one employee of a line rings by turns before the next one.
const RingTurn = 20

// Trunk is an external line.
type Trunk struct {
	ID, Name, Host, Username, AuthUser, Number string
	Password                                   string // "" on saving keeps the old one
	Register                                   bool
	RingMode                                   string   // all | queue
	Ring                                       []string // employees the incoming calls ring
	OutAll                                     bool
	Out                                        []string // who may call out by the line (when not OutAll)
	Position                                   int

	WorkOn           bool   // the line has working hours
	WorkDays         []int  // 1 = Monday … 7 = Sunday
	WorkFrom, WorkTo string // "09:00", the portal's time zone
	NoAnswer         string // hangup | voicemail | forward: a call nobody answered
	NoAnswerTo       string // where it is forwarded
	OffHours         string // the same out of the working hours
	OffHoursTo       string //
	Greeting         []byte // on saving: a new greeting (WAV); nil — the old one stays
	DropGreeting     bool   // on saving: no greeting any more
	GreetingSum      string // the greeting's checksum ("" — none): its file's name
}

// Actions on a call nobody answered (or out of the working hours).
const (
	ActHangup    = "hangup"
	ActVoicemail = "voicemail"
	ActForward   = "forward"
)

// Key names the line's accounts in Asterisk.
func (t Trunk) Key() string { return "trunk-" + strings.ReplaceAll(t.ID, "-", "")[:12] }

var (
	hostRe  = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]{1,5})?$`)
	loginRe = regexp.MustCompile(`^[A-Za-z0-9._+@-]{1,64}$`)
	trunkNo = regexp.MustCompile(`^\+?[0-9]{1,20}$`)
	clockRe = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
)

// Trunks are the external lines in their order, with their passwords.
func (s *Store) Trunks(ctx context.Context) ([]Trunk, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, name, host, username, secret, auth_user, number, register, ring_mode,
		ring_users::text[], out_all, out_users::text[], position, work_on, work_days, work_from, work_to, noanswer, noanswer_to, offhours,
		offhours_to, coalesce(md5(greeting), '') FROM telephony_trunks ORDER BY position, created_at, id`)
	if err != nil {
		return nil, err
	}
	type row struct {
		t   Trunk
		sec []byte
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.t.ID, &x.t.Name, &x.t.Host, &x.t.Username, &x.sec, &x.t.AuthUser, &x.t.Number, &x.t.Register, &x.t.RingMode,
			&x.t.Ring, &x.t.OutAll, &x.t.Out, &x.t.Position, &x.t.WorkOn, &x.t.WorkDays, &x.t.WorkFrom, &x.t.WorkTo, &x.t.NoAnswer,
			&x.t.NoAnswerTo, &x.t.OffHours, &x.t.OffHoursTo, &x.t.GreetingSum)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	list := make([]Trunk, len(got))
	for i, x := range got {
		if x.t.Password, err = s.open(x.sec); err != nil {
			return nil, err
		}
		list[i] = x.t
	}
	return list, nil
}

// SaveTrunk adds (ID "") or changes an external line; its id.
func (s *Store) SaveTrunk(ctx context.Context, userID string, t Trunk) (string, error) {
	t.Name, t.Host, t.Username, t.AuthUser = strings.TrimSpace(t.Name), strings.TrimSpace(t.Host), strings.TrimSpace(t.Username), strings.TrimSpace(t.AuthUser)
	t.Number = strings.Map(func(r rune) rune {
		if strings.ContainsRune(" -().", r) {
			return -1
		}
		return r
	}, t.Number)
	switch {
	case t.Name == "" || len([]rune(t.Name)) > 100 || strings.ContainsAny(t.Name, "\r\n"):
		return "", i18n.E("err.trunk_name")
	case !hostRe.MatchString(t.Host):
		return "", i18n.E("err.trunk_host")
	case !loginRe.MatchString(t.Username) || (t.AuthUser != "" && !loginRe.MatchString(t.AuthUser)):
		return "", i18n.E("err.trunk_login")
	case !trunkNo.MatchString(t.Number):
		return "", i18n.E("err.trunk_number")
	case strings.ContainsAny(t.Password, "\r\n") || len(t.Password) > 128:
		return "", i18n.E("err.trunk_password")
	case t.ID == "" && t.Password == "":
		return "", i18n.E("err.trunk_password")
	}
	if t.RingMode != "queue" {
		t.RingMode = "all"
	}
	var err error
	if t.NoAnswer, t.NoAnswerTo, err = action(t.NoAnswer, t.NoAnswerTo); err != nil {
		return "", err
	}
	if t.OffHours, t.OffHoursTo, err = action(t.OffHours, t.OffHoursTo); err != nil {
		return "", err
	}
	days := []int{}
	for d := 1; d <= 7; d++ {
		if slices.Contains(t.WorkDays, d) {
			days = append(days, d)
		}
	}
	t.WorkDays = days
	if t.WorkFrom == "" && t.WorkTo == "" {
		t.WorkFrom, t.WorkTo = "09:00", "18:00"
	}
	if !clockRe.MatchString(t.WorkFrom) || !clockRe.MatchString(t.WorkTo) || t.WorkFrom >= t.WorkTo || (t.WorkOn && len(days) == 0) {
		return "", i18n.E("err.trunk_hours")
	}
	if t.Greeting != nil {
		if t.Greeting, err = Greeting(t.Greeting); err != nil {
			return "", err
		}
	}
	if t.Ring == nil {
		t.Ring = []string{}
	}
	if t.Out == nil {
		t.Out = []string{}
	}
	var sec []byte
	if t.Password != "" {
		if sec, err = s.seal(t.Password); err != nil {
			return "", err
		}
	}
	rules := []any{t.WorkOn, t.WorkDays, t.WorkFrom, t.WorkTo, t.NoAnswer, t.NoAnswerTo, t.OffHours, t.OffHoursTo, t.Greeting, t.DropGreeting}
	if t.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO telephony_trunks (name, host, username, secret, auth_user, number, register, ring_mode,
			ring_users, out_all, out_users, position, created_by, work_on, work_days, work_from, work_to, noanswer, noanswer_to, offhours, offhours_to, greeting)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::uuid[], $10, $11::uuid[], (SELECT coalesce(max(position), 0) + 1 FROM telephony_trunks), $12,
				$13, $14, $15, $16, $17, $18, $19, $20, CASE WHEN $22 THEN NULL ELSE $21::bytea END)
			RETURNING id::text`, append([]any{t.Name, t.Host, t.Username, sec, t.AuthUser, t.Number, t.Register, t.RingMode, t.Ring, t.OutAll, t.Out, userID},
			rules...)...).Scan(&t.ID)
		return t.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE telephony_trunks SET name = $2, host = $3, username = $4, secret = coalesce($5, secret), auth_user = $6,
		number = $7, register = $8, ring_mode = $9, ring_users = $10::uuid[], out_all = $11, out_users = $12::uuid[], work_on = $13, work_days = $14,
		work_from = $15, work_to = $16, noanswer = $17, noanswer_to = $18, offhours = $19, offhours_to = $20,
		greeting = CASE WHEN $22 THEN NULL ELSE coalesce($21::bytea, greeting) END WHERE id::text = $1`,
		append([]any{t.ID, t.Name, t.Host, t.Username, sec, t.AuthUser, t.Number, t.Register, t.RingMode, t.Ring, t.OutAll, t.Out}, rules...)...)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return t.ID, err
}

// handle is the dialplan of a call nobody answered (or out of the working hours): label is its first priority's.
// busy: the line rings nobody — hanging up is «busy».
func handle(t Trunk, label, act, to string, busy bool, greeting string) string {
	switch act {
	case ActVoicemail:
		return fmt.Sprintf(" same => n%s,Gosub(go24-voicemail,s,1(%s))\n same => n,Hangup()\n", label, greeting)
	case ActForward:
		if len(to) <= innerMax && !strings.HasPrefix(to, "+") { // an employee's inner number
			return fmt.Sprintf(" same => n%s,Dial(Local/%s@go24-ring,45)\n same => n,Hangup()\n", label, to)
		}
		return fmt.Sprintf(" same => n%s,Gosub(go24-forward,s,1(%s,%s,%s))\n same => n,Hangup()\n", label, t.Key(), t.Number, to)
	}
	if busy {
		return fmt.Sprintf(" same => n%s,Busy(5)\n same => n,Hangup()\n", label)
	}
	return fmt.Sprintf(" same => n%s,Hangup()\n", label)
}

// weekdays are the days of the working hours as Asterisk's GotoIfTime wants them: "mon&tue&fri".
func weekdays(days []int) string {
	names := []string{"", "mon", "tue", "wed", "thu", "fri", "sat", "sun"}
	var w []string
	for _, d := range days {
		if d >= 1 && d <= 7 {
			w = append(w, names[d])
		}
	}
	if len(w) == 0 {
		return "*"
	}
	return strings.Join(w, "&")
}

// action checks what happens to a call nobody answered: the action and where it is forwarded ("" unless forwarded).
func action(act, to string) (string, string, error) {
	switch act {
	case ActVoicemail:
		return act, "", nil
	case ActForward:
		to = Dialable(to)
		if !trunkNo.MatchString(to) {
			return "", "", i18n.E("err.trunk_forward")
		}
		return act, to, nil
	}
	return ActHangup, "", nil
}

// TrunkGreeting is the greeting of a line's voicemail (a WAV for Asterisk); nil — none.
func (s *Store) TrunkGreeting(ctx context.Context, id string) ([]byte, error) {
	var g []byte
	err := s.db.QueryRow(ctx, `SELECT greeting FROM telephony_trunks WHERE id::text = $1`, id).Scan(&g)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return g, err
}

// DeleteTrunk removes an external line.
func (s *Store) DeleteTrunk(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM telephony_trunks WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// RenderTrunks is the PJSIP configuration of the external lines (registration, password, the provider's address and
// how its calls are known) and go24's part of the dialplan (go24/extensions.conf): where each line's calls ring and
// which line each employee calls out by. numbers are the employees' inner numbers by their ids.
func RenderTrunks(trunks []Trunk, numbers map[string]string, record bool, tz string) (pjsip, dialplan string) {
	var p, d strings.Builder
	zone := ""
	if tz != "" && tz != "Local" {
		zone = "," + tz
	}
	d.WriteString("; go24 writes this file (Settings → Telephony → External lines): do not edit it by hand\n")
	vars := ""
	if record {
		vars = "set_var = GO24_RECORD=1\n"
	}
	out := map[string]Trunk{} // the employee's number → their line out
	for _, t := range trunks {
		k, host := t.Key(), t.Host
		server, _, _ := strings.Cut(host, ":")
		user := t.AuthUser
		if user == "" {
			user = t.Username
		}
		fmt.Fprintf(&p, "\n; %s\n[%s-auth]\ntype = auth\nauth_type = userpass\nusername = %s\npassword = %s\n", callerName(t.Name), k, user,
			strings.ReplaceAll(t.Password, ";", `\;`)) // «;» starts a comment in Asterisk's files
		fmt.Fprintf(&p, "\n[%s]\ntype = aor\ncontact = sip:%s\nqualify_frequency = 60\n", k, host)
		// From is the provider's login (as providers want), the line's number goes in P-Asserted-Identity
		fmt.Fprintf(&p, "\n[%[1]s]\ntype = endpoint\ncontext = go24-in-%[1]s\ndisallow = all\nallow = alaw,ulaw,g722,opus\noutbound_auth = %[1]s-auth\naors = %[1]s\n"+
			"from_user = %[2]s\nfrom_domain = %[3]s\nsend_pai = yes\ntrust_id_outbound = yes\ndirect_media = no\nrtp_symmetric = yes\nforce_rport = yes\nrewrite_contact = yes\n%[4]s",
			k, t.Username, server, vars)
		fmt.Fprintf(&p, "\n[%s]\ntype = identify\nendpoint = %s\nmatch = %s\n", k, k, server)
		if t.Register {
			fmt.Fprintf(&p, "\n[%[1]s]\ntype = registration\noutbound_auth = %[1]s-auth\nserver_uri = sip:%[2]s\nclient_uri = sip:%[3]s@%[2]s\n"+
				"contact_user = %[4]s\nretry_interval = 60\nforbidden_retry_interval = 600\nexpiration = 300\nline = yes\nendpoint = %[1]s\n",
				k, host, t.Username, strings.TrimPrefix(t.Number, "+"))
		}
		// incoming: whom it rings
		var ring []string
		for _, u := range t.Ring {
			if n := numbers[u]; n != "" {
				ring = append(ring, n)
			}
		}
		fmt.Fprintf(&d, "\n; %s\n[go24-in-%s]\nexten => _X.,1,Goto(s,1)\nexten => _+X.,1,Goto(s,1)\n", callerName(t.Name), k)
		first := ""
		if len(ring) > 0 {
			first = ring[0]
		}
		fmt.Fprintf(&d, "exten => s,1,Gosub(go24-in,s,1(%s,%s,%s))\n", t.Number, first, k)
		if t.WorkOn {
			fmt.Fprintf(&d, " same => n,GotoIfTime(%s-%s,%s,*,*%s?work:off)\n same => n(work),NoOp()\n", t.WorkFrom, t.WorkTo, weekdays(t.WorkDays), zone)
		}
		switch {
		case len(ring) == 0:
		case t.RingMode == "queue":
			for _, n := range ring {
				fmt.Fprintf(&d, " same => n,Dial(Local/%s@go24-ring,%d)\n same => n,GotoIf($[\"${DIALSTATUS}\" = \"ANSWER\"]?done)\n", n, RingTurn)
			}
		default:
			legs := make([]string, len(ring))
			for i, n := range ring {
				legs[i] = "Local/" + n + "@go24-ring"
			}
			fmt.Fprintf(&d, " same => n,Dial(%s,45)\n same => n,GotoIf($[\"${DIALSTATUS}\" = \"ANSWER\"]?done)\n", strings.Join(legs, "&"))
		}
		greeting := ""
		if t.GreetingSum != "" {
			greeting = GreetingFile(t.GreetingSum)
		}
		d.WriteString(handle(t, "", t.NoAnswer, t.NoAnswerTo, len(ring) == 0, greeting))
		if t.WorkOn {
			d.WriteString(handle(t, "(off)", t.OffHours, t.OffHoursTo, false, greeting))
		}
		d.WriteString(" same => n(done),Hangup()\n")
		// outgoing: the first line each employee may call by
		for u, n := range numbers {
			if _, ok := out[n]; ok {
				continue
			}
			if t.OutAll || slices.Contains(t.Out, u) {
				out[n] = t
			}
		}
	}
	d.WriteString("\n[go24-out]\n")
	nums := make([]string, 0, len(out))
	for n := range out {
		nums = append(nums, n)
	}
	slices.Sort(nums)
	for _, n := range nums {
		t := out[n]
		for _, pat := range []string{"_X.", "_+X."} {
			fmt.Fprintf(&d, "exten => %s/%s,1,Gosub(go24-dial-out,s,1(%s,%s,${EXTEN}))\n", pat, n, t.Key(), t.Number)
		}
	}
	d.WriteString("exten => _X.,1,Gosub(go24-no-line,s,1)\nexten => _+X.,1,Gosub(go24-no-line,s,1)\n")
	return p.String(), d.String()
}

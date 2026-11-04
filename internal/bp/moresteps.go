package bp

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"time"

	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/sms"
)

// «Отправка SMS» (sms), «Управление рабочим днём» (workday), «Статус рабочего дня» (workdayinfo) and «Генерация
// случайной строки» (random) of the designer.

// smsStep sends the step's text to its phones ({…} filled, several by a comma) or, with none written, to the first
// phone of the client of the process's CRM card, through the step's provider (the default one when ""). A card's SMS
// is a line of its timeline, as one sent from the card.
func (s *Store) smsStep(ctx context.Context, x Instance, text string, st Step) (string, error) {
	var phones []string
	for _, p := range strings.FieldsFunc(s.fill(ctx, x, st.Value), func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		if p = strings.TrimSpace(p); p != "" {
			phones = append(phones, p)
		}
	}
	card := crm.Entities[x.Entity] && x.EntityID != ""
	if len(phones) == 0 && strings.TrimSpace(st.Value) == "" && card {
		list, err := s.CRM.ClientPhones(ctx, x.Entity, x.EntityID)
		if err != nil {
			return "", err
		}
		if len(list) > 0 {
			phones = []string{list[0].Phone}
		}
	}
	if len(phones) == 0 {
		return "", i18n.E("err.bp_sms_phone")
	}
	for _, phone := range phones {
		var err error
		switch {
		case card:
			err = s.CRM.SendSMSTo(sms.WithSender(ctx, st.Sender), x.StartedBy.ID, x.Entity, x.EntityID, phone, text)
		case s.SMS == nil:
			err = i18n.E("err.sms_no_sender")
		default:
			_, err = s.SMS.Send(ctx, sms.Out{SenderID: st.Sender, Phone: phone, Text: text, AuthorID: x.StartedBy.ID})
			if ie := (*i18n.Error)(nil); err != nil && !errors.As(err, &ie) {
				err = i18n.E("err.sms_failed", err.Error())
			}
		}
		if err != nil {
			return "", err
		}
	}
	return "sms:" + strings.Join(phones, ","), nil
}

// workdayStep starts, pauses, continues or finishes (with the step's text as the report) the working day of today
// of the step's employee. A day already so (started twice, finished, not started to pause) is not a failure: the
// robots pass by too.
func (s *Store) workdayStep(ctx context.Context, x Instance, text string, st Step) (string, error) {
	user, err := s.who(ctx, x, cmp.Or(st.To, "starter"))
	if err != nil {
		return "", err
	}
	did := "workday:" + st.Act + ":" + user
	err = s.Company.WorkAction(ctx, user, st.Act, text, time.Now(), company.Place{})
	if ie := (*i18n.Error)(nil); errors.As(err, &ie) && ie.Key == "err.workday_state" {
		return did + ":same", nil
	}
	return did, err
}

// workdayInfo puts the state of the employee's working day of today into the fields of WorkdayInfo.
func (s *Store) workdayInfo(ctx context.Context, x Instance, st Step) (string, error) {
	user, err := s.who(ctx, x, cmp.Or(st.To, "starter"))
	if err != nil {
		return "", err
	}
	now := time.Now()
	d, err := s.Company.Today(ctx, user, now)
	if err != nil {
		return "", err
	}
	v := map[string]string{"status": "none", "started": "", "ended": "", "worked": "0", "report": ""}
	if d != nil {
		v["status"], v["started"], v["worked"], v["report"] = d.State(), d.Started.In(time.Local).Format("2006-01-02T15:04"),
			strconv.Itoa(d.Worked(now)/60), d.Report
		if d.Ended != nil {
			v["ended"] = d.Ended.In(time.Local).Format("2006-01-02T15:04")
		}
	}
	if x.Values == nil {
		x.Values = map[string]string{}
	}
	for k, val := range v {
		x.Values[WorkdayInfoPrefix+k] = val
	}
	return "workdayinfo:" + user, s.saveValues(ctx, x)
}

// randomChars are the characters of RandomChars; the signs have no braces — a value must not look as {a field}.
var randomChars = map[string]string{"lower": "abcdefghijklmnopqrstuvwxyz", "upper": "ABCDEFGHIJKLMNOPQRSTUVWXYZ",
	"digits": "0123456789", "signs": "!@#$%^&*()-_=+[];:,.?/"}

// randomStep puts a random string of the step's length into its field: of the chosen characters, digits only (the
// first not 0) into a number.
func (s *Store) randomStep(ctx context.Context, x Instance, st Step, kind string) (string, error) {
	var abc string
	for _, c := range RandomChars {
		if len(st.Chars) == 0 && c != "signs" || contains(st.Chars, c) {
			abc += randomChars[c]
		}
	}
	if kind == "number" {
		abc = randomChars["digits"]
	}
	v, err := randomString(abc, st.Size)
	if err != nil {
		return "", err
	}
	if kind == "number" && v[0] == '0' {
		v = "1" + v[1:]
	}
	if x.Values == nil {
		x.Values = map[string]string{}
	}
	x.Values[st.Field] = v
	return "random:" + st.Field, s.saveValues(ctx, x)
}

// randomString is n characters of abc chosen at random (crypto/rand: the string may be a password or a code).
func randomString(abc string, n int) (string, error) {
	b := make([]byte, n)
	max := big.NewInt(int64(len(abc)))
	for i := range b {
		k, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = abc[k.Int64()]
	}
	return string(b), nil
}

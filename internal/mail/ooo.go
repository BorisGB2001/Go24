package mail

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Automatic replies, as Outlook's «Автоматические ответы» («Нет на месте»): on, or for a period; one text for go24's
// people and the user's own domains, another for the rest (nobody, those the user wrote to, everyone). Each sender is
// answered once per period; bulk mail (newsletters, robots, no-reply@ — see Bulk) and the user's own letters never.
// They turn on by themselves while the user is away by go24's absences («График отсутствий»), unless the user turned
// that off, or turned them off from the banner for that absence.

// OOO modes.
const (
	OOOOff    = "off"
	OOOOn     = "on"
	OOOPeriod = "period"
)

// OOO outside modes: whom outside the replies go to.
var OOOOutside = []string{"none", "contacts", "all"}

// OOO are the user's automatic replies.
type OOO struct {
	Mode               string
	From, To           *time.Time // the period (OOOPeriod)
	Internal, External string     // the texts: to go24's people and the user's own domains; to the others
	Outside            string     // one of OOOOutside
	Absence            bool       // on while the user is away (absences)
	Since              time.Time  // each sender is answered once since then
	Dismissed          *time.Time // absences ending by then are left alone (the banner's «Выключить»)
}

// OOOState: are the replies going now, till when, why.
type OOOState struct {
	On      bool
	Until   *time.Time // the period's end, the absence's last day
	Absence string     // the absence's kind, when an absence turned them on
	period  string     // senders are answered once per it
}

// MaxOOOText bounds a reply's text.
const MaxOOOText = 10000

// OOO are the user's automatic replies (the defaults when not set).
func (s *Store) OOO(ctx context.Context, userID string) (OOO, error) {
	o := OOO{Mode: OOOOff, Outside: "all"}
	err := s.db.QueryRow(ctx, `SELECT ooo_mode, ooo_from, ooo_to, ooo_internal, ooo_external, ooo_outside, ooo_absence, ooo_since,
		ooo_dismissed::timestamptz FROM mail_settings WHERE user_id::text = $1`, userID).Scan(&o.Mode, &o.From, &o.To, &o.Internal, &o.External,
		&o.Outside, &o.Absence, &o.Since, &o.Dismissed)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return o, err
}

// SaveOOO keeps the user's automatic replies; senders answered before are answered again.
func (s *Store) SaveOOO(ctx context.Context, userID string, o OOO) error {
	if !oneOf(o.Mode, []string{OOOOff, OOOOn, OOOPeriod}) || !oneOf(o.Outside, OOOOutside) ||
		len([]rune(o.Internal)) > MaxOOOText || len([]rune(o.External)) > MaxOOOText {
		return i18n.E("err.bad_form")
	}
	if o.Mode == OOOPeriod && (o.From == nil || o.To == nil || !o.To.After(*o.From)) {
		return i18n.E("err.mail_ooo_period")
	}
	_, err := s.db.Exec(ctx, `INSERT INTO mail_settings (user_id, ooo_mode, ooo_from, ooo_to, ooo_internal, ooo_external, ooo_outside, ooo_absence)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (user_id) DO UPDATE SET ooo_mode = EXCLUDED.ooo_mode, ooo_from = EXCLUDED.ooo_from, ooo_to = EXCLUDED.ooo_to,
			ooo_internal = EXCLUDED.ooo_internal, ooo_external = EXCLUDED.ooo_external, ooo_outside = EXCLUDED.ooo_outside,
			ooo_absence = EXCLUDED.ooo_absence, ooo_since = now(), ooo_dismissed = NULL`,
		userID, o.Mode, o.From, o.To, strings.TrimSpace(o.Internal), strings.TrimSpace(o.External), o.Outside, o.Absence)
	return err
}

// OOONow: the user's automatic replies and whether they go at the time.
func (s *Store) OOONow(ctx context.Context, userID string, now time.Time) (OOO, OOOState, error) {
	o, err := s.OOO(ctx, userID)
	if err != nil {
		return o, OOOState{}, err
	}
	var st OOOState
	switch {
	case o.Mode == OOOOn:
		st = OOOState{On: true, period: "m:" + strconv.FormatInt(o.Since.Unix(), 10)}
	case o.Mode == OOOPeriod && o.From != nil && o.To != nil && !now.Before(*o.From) && now.Before(*o.To):
		st = OOOState{On: true, Until: o.To, period: "m:" + strconv.FormatInt(o.Since.Unix(), 10)}
	}
	if st.On || !o.Absence {
		return o, st, nil
	}
	var id, kind string
	var ends time.Time
	err = s.db.QueryRow(ctx, `SELECT id::text, kind, ends_on::timestamp FROM absences WHERE user_id::text = $1
			AND $2::date BETWEEN starts_on AND ends_on AND ($3::date IS NULL OR ends_on > $3::date) ORDER BY ends_on DESC LIMIT 1`,
		userID, now.In(time.Local).Format("2006-01-02"), o.Dismissed).Scan(&id, &kind, &ends)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, st, nil
	}
	if err != nil {
		return o, st, err
	}
	ends = time.Date(ends.Year(), ends.Month(), ends.Day(), 0, 0, 0, 0, time.Local)
	return o, OOOState{On: true, Until: &ends, Absence: kind, period: "a:" + id}, nil
}

// OOOOff turns the automatic replies off (the banner's «Выключить»): an absence that turned them on is left alone.
func (s *Store) OOOOff(ctx context.Context, userID string) error {
	_, st, err := s.OOONow(ctx, userID, time.Now())
	if err != nil {
		return err
	}
	var dismissed *string
	if st.Absence != "" && st.Until != nil {
		d := st.Until.Format("2006-01-02")
		dismissed = &d
	}
	_, err = s.db.Exec(ctx, `INSERT INTO mail_settings (user_id, ooo_mode, ooo_dismissed) VALUES ($1, 'off', $2::date)
		ON CONFLICT (user_id) DO UPDATE SET ooo_mode = 'off', ooo_dismissed = coalesce(EXCLUDED.ooo_dismissed, mail_settings.ooo_dismissed)`,
		userID, dismissed)
	return err
}

// text is the reply to a sender (inside: one of go24's people or of the user's domains; wrote: the user wrote to the
// sender); false — none goes ("" — go24's own words).
func (o OOO) text(inside, wrote bool) (string, bool) {
	if inside {
		return o.Internal, true
	}
	if o.Outside == "none" || (o.Outside == "contacts" && !wrote) {
		return "", false
	}
	if strings.TrimSpace(o.External) == "" {
		return o.Internal, true
	}
	return o.External, true
}

// autoReply answers the new letters of a mailbox when the user's automatic replies are on.
func (s *Store) autoReply(ctx context.Context, userID, mailboxID string, news []arrived) error {
	o, st, err := s.OOONow(ctx, userID, time.Now())
	if err != nil || !st.On {
		return err
	}
	rows, err := s.db.Query(ctx, `SELECT lower(email) FROM mailboxes WHERE user_id::text = $1`, userID)
	if err != nil {
		return err
	}
	own, err := pgxCollectStrings(rows)
	if err != nil {
		return err
	}
	domains := map[string]bool{}
	for _, e := range own {
		domains[e[strings.LastIndex(e, "@")+1:]] = true
	}
	var errs []error
	for _, n := range news {
		if n.bulk {
			continue
		}
		m, err := s.message(ctx, userID, n.id)
		if err != nil {
			if !errors.Is(err, ErrNotFound) {
				errs = append(errs, err)
			}
			continue
		}
		from := strings.ToLower(strings.TrimSpace(m.FromAddr))
		at := strings.LastIndex(from, "@")
		if at < 0 || oneOf(from, own) {
			continue
		}
		var employee, wrote bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE active AND lower(email) = $2),
				EXISTS (SELECT 1 FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = f.mailbox_id
					WHERE m.user_id::text = $1 AND f.role = 'sent' AND (x.to_addr || ',' || x.cc_addr) ILIKE $3)`,
			userID, from, likeOf(from)).Scan(&employee, &wrote); err != nil {
			errs = append(errs, err)
			continue
		}
		text, ok := o.text(employee || domains[from[at+1:]], wrote)
		if !ok {
			continue
		}
		if strings.TrimSpace(text) == "" {
			if text = s.tr("mail.ooo.default"); st.Until != nil {
				layout := "02.01.2006"
				if s.Tr != nil {
					layout = s.tr("format.date")
				}
				text = s.tr("mail.ooo.default_until", st.Until.In(time.Local).Format(layout))
			}
		}
		tag, err := s.db.Exec(ctx, `INSERT INTO mail_ooo_sent (user_id, sender, period) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, userID, from, st.period)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if tag.RowsAffected() == 0 { // answered in this period already
			continue
		}
		refs, err := s.refsOf(ctx, userID, m.ID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		errs = append(errs, s.SendLetter(ctx, userID, mailboxID, Letter{To: m.FromAddr, Subject: s.tr("mail.ooo.subject", m.Subject), Text: text,
			Refs: refs, auto: "auto-replied", noCopy: true}))
	}
	return errors.Join(errs...)
}

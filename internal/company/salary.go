package company

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Зарплата и отпуск» of the profile (GO24DEV-895; there it comes from 1C, here an HR officer keeps
// it): the vacation balance of the year — the days the employee is entitled to (the company's default or their own),
// carried over from the last year, taken and planned by the absences «Отпуск», what is left; the payslips of the
// months an HR officer with the right «salary» uploads. The balance is seen by the employee, their managers, HR
// officers and administrators; a payslip — by the employee, HR officers with the right and administrators.

// DefaultVacationDays is the yearly vacation before any setting (28 calendar days, as the Labour Code has it).
const DefaultVacationDays = 28

const settingVacation = "vacation_settings"

// VacationSettings is the company's default of the yearly vacation.
type VacationSettings struct {
	Days int `json:"days"`
}

// VacationSettings returns the company's default.
func (s *Store) VacationSettings(ctx context.Context) (VacationSettings, error) {
	set := VacationSettings{Days: DefaultVacationDays}
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = $1`, settingVacation).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return set, nil
	}
	if err != nil {
		return set, err
	}
	err = json.Unmarshal(raw, &set)
	return set, err
}

// SaveVacationSettings sets the company's default; who may — the caller checks (CanVacation).
func (s *Store) SaveVacationSettings(ctx context.Context, set VacationSettings, by string) error {
	if set.Days < 0 || set.Days > 366 {
		return i18n.E("err.vacation_days")
	}
	raw, err := json.Marshal(set)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2, NULLIF($3, '')::uuid)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		settingVacation, raw, by)
	return err
}

// CanVacation: who keeps the vacation days of anyone — administrators and HR officers with the right «absences».
func (s *Store) CanVacation(ctx context.Context, userID string, admin bool) (bool, error) {
	if admin {
		return true, nil
	}
	h, err := s.HROf(ctx, userID)
	return h.Can("absences"), err
}

// CanSalary: who uploads and sees anyone's payslips — administrators and HR officers with the right «salary».
func (s *Store) CanSalary(ctx context.Context, userID string, admin bool) (bool, error) {
	if admin {
		return true, nil
	}
	h, err := s.HROf(ctx, userID)
	return h.Can("salary"), err
}

// SeesVacation: the employee themself, their managers (canManage), any HR officer and administrators.
func (s *Store) SeesVacation(ctx context.Context, actor, userID string, admin bool) (bool, error) {
	if ok, err := s.canManage(ctx, actor, userID, admin); err != nil || ok {
		return ok, err
	}
	h, err := s.HROf(ctx, actor)
	return h.On, err
}

// VacationBalance is an employee's vacation of a year, in calendar days.
type VacationBalance struct {
	UserID  string `json:"user_id"`
	Year    int    `json:"year"`
	Days    int    `json:"days"`     // entitled to in the year
	Own     bool   `json:"own"`      // Days is the employee's own, not the company's default
	Carried int    `json:"carried"`  // carried over from the last year (may be negative: taken in advance)
	Taken   int    `json:"taken"`    // the vacations of the year before today
	Planned int    `json:"planned"`  // from today on
	Left    int    `json:"left"`     // Days + Carried − Taken − Planned
	LeftNow int    `json:"left_now"` // Days + Carried − Taken: what is left without the plan
}

// VacationBalances returns the balances of the year of the people (all active employees when ids is nil), as of the
// day.
func (s *Store) VacationBalances(ctx context.Context, year int, ids []string, today time.Time) (map[string]VacationBalance, error) {
	set, err := s.VacationSettings(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT u.id::text, b.days, coalesce(b.carried, 0) FROM users u
		LEFT JOIN vacation_balances b ON b.user_id = u.id AND b.year = $1
		WHERE u.active AND NOT u.guest AND ($2::text[] IS NULL OR u.id::text = ANY($2))`, year, ids)
	if err != nil {
		return nil, err
	}
	out := map[string]VacationBalance{}
	for rows.Next() {
		var b VacationBalance
		var days *int
		if err := rows.Scan(&b.UserID, &days, &b.Carried); err != nil {
			rows.Close()
			return nil, err
		}
		b.Year, b.Days = year, set.Days
		if days != nil {
			b.Days, b.Own = *days, true
		}
		out[b.UserID] = b
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	first, last := time.Date(year, 1, 1, 0, 0, 0, 0, time.Local), time.Date(year, 12, 31, 0, 0, 0, 0, time.Local)
	list, err := s.Absences(ctx, first, last)
	if err != nil {
		return nil, err
	}
	today = localDay(today)
	for _, a := range list {
		b, ok := out[a.UserID]
		if !ok || a.Kind != "vacation" {
			continue
		}
		taken, planned := vacationSplit(a.From, a.To, first, last, today)
		b.Taken, b.Planned = b.Taken+taken, b.Planned+planned
		out[a.UserID] = b
	}
	for id, b := range out {
		b.LeftNow = b.Days + b.Carried - b.Taken
		b.Left = b.LeftNow - b.Planned
		out[id] = b
	}
	return out, nil
}

// vacationSplit: the calendar days of a vacation within the year (first…last) before the day and from it on.
func vacationSplit(from, to, first, last, today time.Time) (taken, planned int) {
	from, to = localDay(from), localDay(to)
	if from.Before(first) {
		from = first
	}
	if to.After(last) {
		to = last
	}
	days := func(a, b time.Time) int {
		if b.Before(a) {
			return 0
		}
		return int(b.Sub(a).Hours()/24+0.5) + 1
	}
	if today.After(to) {
		return days(from, to), 0
	}
	if !today.After(from) {
		return 0, days(from, to)
	}
	return days(from, today.AddDate(0, 0, -1)), days(today, to)
}

// VacationBalanceOf is one employee's balance of the year.
func (s *Store) VacationBalanceOf(ctx context.Context, userID string, year int, today time.Time) (VacationBalance, error) {
	m, err := s.VacationBalances(ctx, year, []string{userID}, today)
	if err != nil {
		return VacationBalance{}, err
	}
	b, ok := m[userID]
	if !ok {
		return VacationBalance{}, ErrNotFound
	}
	return b, nil
}

// SetVacationBalance sets the employee's own days of the year (nil — the company's default) and the days carried
// over; who may — the caller checks (CanVacation).
func (s *Store) SetVacationBalance(ctx context.Context, actor, userID string, year int, days *int, carried int) error {
	if year < 2000 || year > 2100 || (days != nil && (*days < 0 || *days > 366)) || carried < -366 || carried > 366 {
		return i18n.E("err.vacation_days")
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO vacation_balances (user_id, year, days, carried, updated_by)
		SELECT id, $2, $3, $4, NULLIF($5, '')::uuid FROM users WHERE id::text = $1 AND active AND NOT guest
		ON CONFLICT (user_id, year) DO UPDATE SET days = EXCLUDED.days, carried = EXCLUDED.carried,
			updated_by = EXCLUDED.updated_by, updated_at = now()`, userID, year, days, carried, actor)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// Payslip is the payslip of a month.
type Payslip struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	UserName   string     `json:"user_name"`
	Month      time.Time  `json:"month"` // the first day
	FileKey    string     `json:"-"`
	FileName   string     `json:"file_name"`
	Size       int64      `json:"size"`
	UploadedBy string     `json:"uploaded_by"`
	Created    time.Time  `json:"created_at"`
	Seen       *time.Time `json:"seen_at"` // the employee opened it
}

// SeenDay is when the employee opened it (zero — not yet).
func (p Payslip) SeenDay() time.Time {
	if p.Seen == nil {
		return time.Time{}
	}
	return *p.Seen
}

// PayslipFilter narrows the payslips.
type PayslipFilter struct {
	UserID string
	Month  time.Time // zero — any
}

const payslipSQL = `SELECT p.id::text, p.user_id::text, u.name, p.month, p.file_key, p.file_name, p.size,
		coalesce(p.uploaded_by::text, ''), p.created_at, p.seen_at
	FROM payslips p JOIN users u ON u.id = p.user_id`

func scanPayslips(rows pgx.Rows, err error) ([]Payslip, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Payslip, error) {
		var p Payslip
		err := row.Scan(&p.ID, &p.UserID, &p.UserName, &p.Month, &p.FileKey, &p.FileName, &p.Size, &p.UploadedBy, &p.Created, &p.Seen)
		p.Month = localDay(p.Month) // a date comes as UTC midnight
		return p, err
	})
}

// Payslips lists the payslips, the latest month first; who may see whose — the caller checks.
func (s *Store) Payslips(ctx context.Context, f PayslipFilter) ([]Payslip, error) {
	where, args := []string{"true"}, []any{}
	if f.UserID != "" {
		args = append(args, f.UserID)
		where = append(where, "p.user_id::text = $1")
	}
	if !f.Month.IsZero() {
		args = append(args, monthOf(f.Month).Format(time.DateOnly))
		where = append(where, "p.month = $"+strconv.Itoa(len(args))+"::date")
	}
	return scanPayslips(s.db.Query(ctx, payslipSQL+" WHERE "+strings.Join(where, " AND ")+" ORDER BY p.month DESC, u.name LIMIT 2000", args...))
}

// Payslip returns one payslip.
func (s *Store) Payslip(ctx context.Context, id string) (Payslip, error) {
	list, err := scanPayslips(s.db.Query(ctx, payslipSQL+" WHERE p.id::text = $1", id))
	if err != nil {
		return Payslip{}, err
	}
	if len(list) == 0 {
		return Payslip{}, ErrNotFound
	}
	return list[0], nil
}

func monthOf(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.Local) }

// SavePayslip keeps the stored file as the employee's payslip of the month (the one there was is replaced) and tells
// the employee; who may — the caller checks (CanSalary). It returns the payslip's ID.
func (s *Store) SavePayslip(ctx context.Context, actor, userID string, month time.Time, key, name string, size int64) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || key == "" || month.Year() < 2000 || month.Year() > 2100 {
		return "", i18n.E("err.bad_form")
	}
	m := monthOf(month)
	var id string
	err := s.db.QueryRow(ctx, `INSERT INTO payslips (user_id, month, file_key, file_name, size, uploaded_by)
		SELECT id, $2::date, $3, $4, $5, NULLIF($6, '')::uuid FROM users WHERE id::text = $1 AND active AND NOT guest
		ON CONFLICT (user_id, month) DO UPDATE SET file_key = EXCLUDED.file_key, file_name = EXCLUDED.file_name,
			size = EXCLUDED.size, uploaded_by = EXCLUDED.uploaded_by, created_at = now(), seen_at = NULL
		RETURNING id::text`, userID, m.Format(time.DateOnly), key, name, size, actor).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url) VALUES ($1, NULLIF($2, '')::uuid, 'payslip', $3, $4)`,
		userID, actor, m.Format("01.2006"), "/u/"+userID+"#psalary")
	return id, err
}

// SeePayslip notes that the employee opened their payslip (the first time).
func (s *Store) SeePayslip(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `UPDATE payslips SET seen_at = now() WHERE id::text = $1 AND seen_at IS NULL`, id)
	return err
}

// DeletePayslip removes a payslip; who may — the caller checks (CanSalary).
func (s *Store) DeletePayslip(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM payslips WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// MatchPayslipFile finds whose payslip a file is by its name: the e-mail in it, or every word of the person's name
// (case, «ё» and separators aside: «Иванов_Пётр_10-2026.pdf» is Пётр Иванов's). No one or several people — "".
func MatchPayslipFile(file string, people []PayslipPerson) string {
	low := strings.ToLower(file)
	for _, p := range people {
		if p.Email != "" && hasEmail(low, strings.ToLower(p.Email)) {
			return p.ID
		}
	}
	words := map[string]bool{}
	for _, w := range nameWords(strings.TrimSuffix(low, ".pdf")) {
		words[w] = true
	}
	found := ""
	for _, p := range people {
		name := nameWords(p.Name)
		if len(name) < 2 {
			continue
		}
		all := true
		for _, w := range name {
			all = all && words[w]
		}
		if all {
			if found != "" {
				return "" // two namesakes: the officer picks
			}
			found = p.ID
		}
	}
	return found
}

// hasEmail: the e-mail is in the text as a whole («a@corp.ru» is not in «anna@corp.ru»).
func hasEmail(text, email string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], email)
		if j < 0 {
			return false
		}
		at := i + j
		if at == 0 || !strings.ContainsRune("abcdefghijklmnopqrstuvwxyz0123456789._%+-", rune(text[at-1])) {
			return true
		}
		i = at + 1
	}
}

func nameWords(s string) []string {
	s = strings.ReplaceAll(strings.ToLower(s), "ё", "е")
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) })
}

// PayslipPerson is an employee the files are matched with.
type PayslipPerson struct {
	ID, Name, Email string
}

// PayslipPeople are the active employees with their e-mails.
func (s *Store) PayslipPeople(ctx context.Context) ([]PayslipPerson, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, name, coalesce(email, '') FROM users WHERE active AND NOT guest AND oidc_sub <> 'legacy:0' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[PayslipPerson])
}

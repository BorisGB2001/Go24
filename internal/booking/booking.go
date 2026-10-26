// Package booking is «Онлайн-запись»: resources of the company (employees, rooms, equipment) with their
// working schedule, and bookings of clients on them, kept from overlapping.
package booking

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
)

var (
	ErrNotFound  = i18n.E("err.not_found")
	ErrName      = i18n.E("booking.err.name")
	ErrSchedule  = i18n.E("booking.err.schedule")
	ErrConfirm   = i18n.E("booking.err.confirm")
	ErrTime      = i18n.E("booking.err.time")
	ErrClient    = i18n.E("booking.err.client")
	ErrOverlap   = i18n.E("booking.err.overlap")
	ErrTypeInUse = i18n.E("booking.err.type_in_use")
)

// Statuses of a booking; a cancelled one frees its time.
var Statuses = []string{"unconfirmed", "confirmed", "came", "no_show", "cancelled"}

type Store struct{ db *pgxpool.Pool }

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// Type is a resource type: a standard one has a Code (its name is the locale's), the others a Name.
type Type struct {
	ID, Code, Name string
	Resources      int
}

// Resource is what is booked; it works on Days (ISO weekdays) From–To (minutes of the day; DayHours — of its own for
// some weekdays) by slots of Slot minutes, but for the break BreakFrom–BreakTo and the dates off.
type Resource struct {
	ID, TypeID, TypeCode, TypeName string
	Name, Description              string
	UserID, UserName               string // the employee the resource is
	Slot                           int
	Days                           []int
	From, To                       int
	NotifyMail                     bool           // letters to the client: about the booking, the reminder, the lateness
	RemindHours, LateMinutes       int            // the reminder that many hours before, the lateness note that many minutes after; 0 — none
	ConfirmHours, ConfirmAlert     int            // ask the client to confirm that many hours before; tell the employee that many hours before if not; 0 — none
	Overbooking                    bool           // bookings of employees may overlap (by a link — never)
	Feedback                       bool           // after a visit the client is asked by a letter to rate it
	Services                       []Service      // the catalog's services it provides, with their minutes
	DayHours                       map[int][2]int // an ISO weekday → its own from, to
	BreakFrom, BreakTo             int            // equal — no break
	OffDates                       []time.Time    // the dates it does not work, in order
	CalendarBusy                   bool           // an employee: their meetings and absences in the calendar take its slots
}

func isoWeekday(t time.Time) int {
	if wd := int(t.Weekday()); wd != 0 {
		return wd
	}
	return 7
}

// WorksOn: the resource works on the day of t — a working weekday, not a date off.
func (r Resource) WorksOn(t time.Time) bool {
	return slices.Contains(r.Days, isoWeekday(t)) && !r.IsOff(t)
}

// IsOff: the day of t is a date off of the resource.
func (r Resource) IsOff(t time.Time) bool {
	return slices.ContainsFunc(r.OffDates, func(d time.Time) bool {
		return d.Year() == t.Year() && d.Month() == t.Month() && d.Day() == t.Day()
	})
}

// HoursOn: the working hours (minutes of the day) on the day of t — of its weekday or the common ones.
func (r Resource) HoursOn(t time.Time) (from, to int) {
	if h, ok := r.DayHours[isoWeekday(t)]; ok {
		return h[0], h[1]
	}
	return r.From, r.To
}

// InBreak: the minutes a–b of a day touch the break.
func (r Resource) InBreak(a, b int) bool {
	return r.BreakFrom < r.BreakTo && a < r.BreakTo && b > r.BreakFrom
}

// OwnFrom, OwnTo: the weekday's own hours as "09:00", "15:00" for the form; "" — the common ones.
func (r Resource) OwnFrom(wd int) string { return r.own(wd, 0) }
func (r Resource) OwnTo(wd int) string   { return r.own(wd, 1) }

func (r Resource) own(wd, i int) string {
	h, ok := r.DayHours[wd]
	if !ok {
		return ""
	}
	return fmt.Sprintf("%02d:%02d", h[i]/60, h[i]%60)
}

// Booking is a client's visit to a resource.
type Booking struct {
	ID, ResourceID           string
	Start, End               time.Time
	ClientName, ClientPhone  string
	ClientEmail, Note        string
	Status                   string
	CreatedBy, CreatedByName string
	LinkID, LinkName         string // the link the client booked by themselves
	ClientIP, ClientToken    string // of a booking by a link: the client's address and the token of its page
	ContactID, ContactName   string // the client's cards in CRM, each one or none
	CompanyID, CompanyName   string
	DealID, DealTitle        string
	ResourceName             string
	Extras, ExtraNames       []string  // the other resources the booking takes at the same time
	Services                 []Service // what the client comes for, as booked
	Rating                   int       // the client's rating of the visit, 1–5; 0 — none
	RatingNote               string    // and their comment
}

func (s *Store) Types(ctx context.Context) ([]Type, error) {
	rows, err := s.db.Query(ctx, `SELECT t.id, coalesce(t.code, ''), t.name,
			(SELECT count(*) FROM booking_resources r WHERE r.type_id = t.id)::int
		FROM booking_resource_types t ORDER BY t.code IS NULL, t.created_at, t.name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Type, error) {
		var t Type
		return t, r.Scan(&t.ID, &t.Code, &t.Name, &t.Resources)
	})
}

// SaveType adds a type of its own (no id) or renames one; the standard ones keep their names.
func (s *Store) SaveType(ctx context.Context, id, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ErrName
	}
	if id == "" {
		return id, s.db.QueryRow(ctx, `INSERT INTO booking_resource_types (name) VALUES ($1) RETURNING id`, name).Scan(&id)
	}
	tag, err := s.db.Exec(ctx, `UPDATE booking_resource_types SET name = $2 WHERE id = $1 AND code IS NULL`, id, name)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return id, err
}

// DeleteType removes a type of its own that no resource has.
func (s *Store) DeleteType(ctx context.Context, id string) error {
	var used bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM booking_resources WHERE type_id = $1)`, id).Scan(&used); err != nil {
		return err
	}
	if used {
		return ErrTypeInUse
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM booking_resource_types WHERE id = $1 AND code IS NULL`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

const resourceSQL = `SELECT r.id, r.type_id, coalesce(t.code, ''), t.name, r.name, r.description,
		coalesce(r.user_id::text, ''), coalesce(u.name, ''), r.slot_minutes, r.work_days, r.work_from, r.work_to,
		r.notify_mail, r.remind_hours, r.late_minutes, r.overbooking, r.feedback, r.day_hours, r.break_from, r.break_to,
		r.off_dates, r.confirm_hours, r.confirm_alert_hours, r.calendar_busy
	FROM booking_resources r JOIN booking_resource_types t ON t.id = r.type_id LEFT JOIN users u ON u.id = r.user_id`

func scanResource(r pgx.Row) (Resource, error) {
	var x Resource
	err := r.Scan(&x.ID, &x.TypeID, &x.TypeCode, &x.TypeName, &x.Name, &x.Description, &x.UserID, &x.UserName,
		&x.Slot, &x.Days, &x.From, &x.To, &x.NotifyMail, &x.RemindHours, &x.LateMinutes, &x.Overbooking, &x.Feedback,
		&x.DayHours, &x.BreakFrom, &x.BreakTo, &x.OffDates, &x.ConfirmHours, &x.ConfirmAlert, &x.CalendarBusy)
	return x, err
}

// Resources: all of them, by type and name; typeID — only those of one type.
func (s *Store) Resources(ctx context.Context, typeID string) ([]Resource, error) {
	rows, err := s.db.Query(ctx, resourceSQL+` WHERE $1 = '' OR r.type_id::text = $1
		ORDER BY t.code IS NULL, t.created_at, lower(r.name)`, typeID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Resource, error) { return scanResource(r) })
	if err == nil {
		err = s.withServices(ctx, list)
	}
	return list, err
}

func (s *Store) Resource(ctx context.Context, id string) (Resource, error) {
	r, err := scanResource(s.db.QueryRow(ctx, resourceSQL+` WHERE r.id::text = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	if err == nil {
		list := []Resource{r}
		err = s.withServices(ctx, list)
		r = list[0]
	}
	return r, err
}

// SaveResource adds a resource (no ID) or changes one, with its services.
func (s *Store) SaveResource(ctx context.Context, by string, r Resource) (string, error) {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return "", ErrName
	}
	days := []int{}
	for _, d := range r.Days {
		if d >= 1 && d <= 7 && !slices.Contains(days, d) {
			days = append(days, d)
		}
	}
	slices.Sort(days)
	if r.Slot < 5 || r.Slot > 480 || r.From < 0 || r.To > 1440 || r.From >= r.To || len(days) == 0 ||
		r.RemindHours < 0 || r.RemindHours > 336 || r.LateMinutes < 0 || r.LateMinutes > 240 ||
		r.BreakFrom < 0 || r.BreakTo > 1440 || r.BreakFrom > r.BreakTo {
		return "", ErrSchedule
	}
	if r.ConfirmHours < 0 || r.ConfirmHours > 336 || r.ConfirmAlert < 0 || (r.ConfirmAlert > 0 && r.ConfirmAlert >= r.ConfirmHours) {
		return "", ErrConfirm
	}
	hours := map[int][2]int{}
	for wd, h := range r.DayHours {
		if !slices.Contains(days, wd) || h == [2]int{r.From, r.To} {
			continue // not a working day or the common hours
		}
		if h[0] < 0 || h[1] > 1440 || h[0] >= h[1] {
			return "", ErrSchedule
		}
		hours[wd] = h
	}
	if r.BreakFrom == r.BreakTo {
		r.BreakFrom, r.BreakTo = 0, 0
	}
	off := []time.Time{}
	for _, d := range r.OffDates {
		d = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
		if !slices.ContainsFunc(off, d.Equal) {
			off = append(off, d)
		}
	}
	slices.SortFunc(off, time.Time.Compare)
	user := any(nil)
	if r.UserID != "" {
		user = r.UserID
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if r.ID == "" {
		err = tx.QueryRow(ctx, `INSERT INTO booking_resources (type_id, name, description, user_id, slot_minutes,
				work_days, work_from, work_to, created_by, notify_mail, remind_hours, late_minutes, overbooking, feedback,
				day_hours, break_from, break_to, off_dates, confirm_hours, confirm_alert_hours, calendar_busy)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21) RETURNING id`,
			r.TypeID, r.Name, r.Description, user, r.Slot, days, r.From, r.To, by, r.NotifyMail, r.RemindHours, r.LateMinutes,
			r.Overbooking, r.Feedback, hours, r.BreakFrom, r.BreakTo, off, r.ConfirmHours, r.ConfirmAlert, r.CalendarBusy).Scan(&r.ID)
	} else {
		var tag pgconn.CommandTag
		tag, err = tx.Exec(ctx, `UPDATE booking_resources SET type_id = $2, name = $3, description = $4, user_id = $5,
			slot_minutes = $6, work_days = $7, work_from = $8, work_to = $9, notify_mail = $10, remind_hours = $11,
			late_minutes = $12, overbooking = $13, feedback = $14, day_hours = $15, break_from = $16, break_to = $17,
			off_dates = $18, confirm_hours = $19, confirm_alert_hours = $20, calendar_busy = $21 WHERE id = $1`,
			r.ID, r.TypeID, r.Name, r.Description, user, r.Slot, days, r.From, r.To, r.NotifyMail, r.RemindHours, r.LateMinutes,
			r.Overbooking, r.Feedback, hours, r.BreakFrom, r.BreakTo, off, r.ConfirmHours, r.ConfirmAlert, r.CalendarBusy)
		if err == nil && tag.RowsAffected() == 0 {
			err = ErrNotFound
		}
	}
	if err == nil {
		err = saveServices(ctx, tx, r)
	}
	if err != nil {
		return "", err
	}
	return r.ID, tx.Commit(ctx)
}

// DeleteResource removes a resource with its bookings.
func (s *Store) DeleteResource(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM booking_resources WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

const bookingSQL = `SELECT b.id, b.resource_id, b.starts_at, b.ends_at, b.client_name, b.client_phone, b.client_email,
		b.note, b.status, coalesce(b.created_by::text, ''), coalesce(u.name, ''), coalesce(b.link_id::text, ''),
		coalesce(l.name, ''), b.client_ip, coalesce(b.client_token, ''), coalesce(b.contact_id::text, ''), coalesce(x.name, ''),
		coalesce(b.company_id::text, ''), coalesce(c.title, ''), coalesce(b.deal_id::text, ''), coalesce(d.title, ''), r.name,
		ARRAY(SELECT e.resource_id::text FROM booking_extra_resources e JOIN booking_resources er ON er.id = e.resource_id
			WHERE e.booking_id = b.id ORDER BY lower(er.name)),
		ARRAY(SELECT er.name FROM booking_extra_resources e JOIN booking_resources er ON er.id = e.resource_id
			WHERE e.booking_id = b.id ORDER BY lower(er.name)),
		coalesce(b.rating, 0), b.rating_note
	FROM bookings b JOIN booking_resources r ON r.id = b.resource_id LEFT JOIN users u ON u.id = b.created_by
	LEFT JOIN booking_links l ON l.id = b.link_id LEFT JOIN crm_contacts x ON x.id = b.contact_id
	LEFT JOIN crm_companies c ON c.id = b.company_id LEFT JOIN crm_deals d ON d.id = b.deal_id`

func scanBooking(r pgx.Row) (Booking, error) {
	var b Booking
	err := r.Scan(&b.ID, &b.ResourceID, &b.Start, &b.End, &b.ClientName, &b.ClientPhone, &b.ClientEmail, &b.Note,
		&b.Status, &b.CreatedBy, &b.CreatedByName, &b.LinkID, &b.LinkName, &b.ClientIP, &b.ClientToken, &b.ContactID,
		&b.ContactName, &b.CompanyID, &b.CompanyName, &b.DealID, &b.DealTitle, &b.ResourceName, &b.Extras, &b.ExtraNames,
		&b.Rating, &b.RatingNote)
	return b, err
}

// OfCard: the bookings of a CRM card (entity contact, company or deal), the latest first.
func (s *Store) OfCard(ctx context.Context, entity, id string) ([]Booking, error) {
	col := map[string]string{"contact": "contact_id", "company": "company_id", "deal": "deal_id"}[entity]
	if col == "" {
		return nil, nil
	}
	return s.bookings(ctx, bookingSQL+` WHERE b.`+col+`::text = $1 ORDER BY b.starts_at DESC LIMIT 100`, id)
}

// Takes: the booking takes the resource, as its own or one of the others.
func (b Booking) Takes(id string) bool { return b.ResourceID == id || slices.Contains(b.Extras, id) }

// Bookings: those that touch [from, to), cancelled ones too, by time.
func (s *Store) Bookings(ctx context.Context, from, to time.Time) ([]Booking, error) {
	return s.bookings(ctx, bookingSQL+` WHERE b.starts_at < $2 AND b.ends_at > $1 ORDER BY b.starts_at`, from, to)
}

// bookings by a query of bookingSQL, with their services.
func (s *Store) bookings(ctx context.Context, sql string, args ...any) ([]Booking, error) {
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Booking, error) { return scanBooking(r) })
	if err == nil {
		err = s.bookingServices(ctx, list)
	}
	return list, err
}

func (s *Store) Booking(ctx context.Context, id string) (Booking, error) {
	return s.oneBooking(ctx, bookingSQL+` WHERE b.id::text = $1`, id)
}

// oneBooking by a query of bookingSQL, with its services.
func (s *Store) oneBooking(ctx context.Context, sql string, args ...any) (Booking, error) {
	list, err := s.bookings(ctx, sql, args...)
	if err == nil && len(list) == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return Booking{}, err
	}
	return list[0], nil
}

// SaveBooking adds a booking (no ID, «не подтверждена» unless Status says otherwise) or changes one. It takes its
// resource and the Extras; its time must not overlap another booking (not cancelled) of any of them that does not
// allow overbooking: the resource rows are locked while it is checked, so two employees cannot take the same time at once.
func (s *Store) SaveBooking(ctx context.Context, by string, b Booking) (string, error) {
	return s.save(ctx, by, b, false)
}

func (s *Store) save(ctx context.Context, by string, b Booking, strict bool) (string, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if b.ID, err = saveBooking(ctx, tx, by, b, strict); err != nil {
		return "", err
	}
	return b.ID, tx.Commit(ctx)
}

// saveBooking is SaveBooking in tx; strict — overbooking is not allowed (a client booking by a link).
func saveBooking(ctx context.Context, tx pgx.Tx, by string, b Booking, strict bool) (string, error) {
	b.ClientName, b.ClientPhone, b.ClientEmail = strings.TrimSpace(b.ClientName), strings.TrimSpace(b.ClientPhone), strings.TrimSpace(b.ClientEmail)
	if b.ClientName == "" && b.ClientPhone == "" {
		return "", ErrClient
	}
	if !b.Start.Before(b.End) {
		return "", ErrTime
	}
	if b.Status == "" {
		b.Status = "unconfirmed"
	}
	if !slices.Contains(Statuses, b.Status) {
		return "", ErrNotFound
	}
	extras := []string{}
	for _, id := range b.Extras {
		if id != "" && id != b.ResourceID && !slices.Contains(extras, id) {
			extras = append(extras, id)
		}
	}
	ids := append([]string{b.ResourceID}, extras...)
	rows, err := tx.Query(ctx, `SELECT id::text, overbooking FROM booking_resources WHERE id::text = ANY($1) ORDER BY id FOR UPDATE`, ids)
	if err != nil {
		return "", err
	}
	check := []string{} // the resources that must be free
	n := 0
	for rows.Next() {
		var id string
		var over bool
		if err := rows.Scan(&id, &over); err != nil {
			rows.Close()
			return "", err
		}
		n++
		if strict || !over {
			check = append(check, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	if n != len(ids) {
		return "", ErrNotFound
	}
	if b.Status != "cancelled" && len(check) > 0 {
		var busy bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM bookings x WHERE x.status <> 'cancelled'
				AND x.starts_at < $3 AND x.ends_at > $2 AND x.id::text <> $4 AND (x.resource_id::text = ANY($1)
				OR EXISTS (SELECT 1 FROM booking_extra_resources e WHERE e.booking_id = x.id AND e.resource_id::text = ANY($1))))`,
			check, b.Start, b.End, b.ID).Scan(&busy); err != nil {
			return "", err
		}
		if busy {
			return "", ErrOverlap
		}
	}
	if b.ID == "" {
		if b.ClientToken == "" { // every booking has the client's page: the letters link to it
			b.ClientToken = token()
		}
		err = tx.QueryRow(ctx, `INSERT INTO bookings (resource_id, starts_at, ends_at, client_name, client_phone, client_email,
				note, status, created_by, link_id, client_ip, client_token, contact_id, company_id, deal_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) RETURNING id`,
			b.ResourceID, b.Start, b.End, b.ClientName, b.ClientPhone, b.ClientEmail, b.Note, b.Status, nullable(by),
			nullable(b.LinkID), b.ClientIP, nullable(b.ClientToken), nullable(b.ContactID), nullable(b.CompanyID),
			nullable(b.DealID)).Scan(&b.ID)
	} else {
		var tag pgconn.CommandTag
		tag, err = tx.Exec(ctx, `UPDATE bookings SET resource_id = $2, starts_at = $3, ends_at = $4, client_name = $5,
				client_phone = $6, client_email = $7, note = $8, status = $9, contact_id = $10, company_id = $11,
				deal_id = $12, updated_at = now(),
				reminded_at = CASE WHEN starts_at = $3 THEN reminded_at END, late_at = CASE WHEN starts_at = $3 THEN late_at END,
				confirm_asked_at = CASE WHEN starts_at = $3 THEN confirm_asked_at END,
				unconfirmed_at = CASE WHEN starts_at = $3 THEN unconfirmed_at END
				WHERE id::text = $1`,
			b.ID, b.ResourceID, b.Start, b.End, b.ClientName, b.ClientPhone, b.ClientEmail, b.Note, b.Status,
			nullable(b.ContactID), nullable(b.CompanyID), nullable(b.DealID))
		if err == nil && tag.RowsAffected() == 0 {
			err = ErrNotFound
		}
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM booking_extra_resources WHERE booking_id = $1`, b.ID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO booking_extra_resources (booking_id, resource_id)
			SELECT $1, unnest($2::uuid[])`, b.ID, extras); err != nil {
		return "", err
	}
	if err := saveBookingServices(ctx, tx, b); err != nil {
		return "", err
	}
	return b.ID, nil
}

// nullable: "" is NULL.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// SetStatus changes the status of a booking; one brought back from «отменена» must still find its time free.
func (s *Store) SetStatus(ctx context.Context, by, id, status string) error {
	b, err := s.Booking(ctx, id)
	if err != nil {
		return err
	}
	b.Status = status
	_, err = s.SaveBooking(ctx, by, b)
	return err
}

func (s *Store) DeleteBooking(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM bookings WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// Free: the free slots of a resource on a day (in loc) — those of its schedule that no booking (not cancelled)
// touches, as its own resource or one of the others, and that are not in the past; overbooking or not.
func (s *Store) Free(ctx context.Context, r Resource, day time.Time, loc *time.Location) ([]time.Time, error) {
	return s.FreeFor(ctx, r, day, loc, r.Slot)
}

// FreeFor: the starts of the resource's slots on a day (in loc) from which mins minutes are free (a service longer
// than a slot takes the following ones) and end within its working hours of that day, not touching its break nor
// the busy time of its employee's calendar (see CalendarBusy).
func (s *Store) FreeFor(ctx context.Context, r Resource, day time.Time, loc *time.Location, mins int) ([]time.Time, error) {
	if mins <= 0 {
		mins = r.Slot
	}
	d0 := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	if !r.WorksOn(d0) {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `SELECT starts_at, ends_at FROM bookings b WHERE status <> 'cancelled' AND starts_at < $3
		AND ends_at > $2 AND (resource_id = $1 OR EXISTS (SELECT 1 FROM booking_extra_resources e WHERE e.booking_id = b.id
		AND e.resource_id = $1))`, r.ID, d0, d0.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	type span struct{ a, b time.Time }
	busy, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (span, error) {
		var x span
		return x, row.Scan(&x.a, &x.b)
	})
	if err != nil {
		return nil, err
	}
	cal, err := s.CalendarBusy(ctx, r, d0, d0.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	for _, x := range cal {
		busy = append(busy, span{x.Start, x.End})
	}
	now := time.Now()
	var free []time.Time
	from, to := r.HoursOn(d0)
	for m := from; m+mins <= to; m += r.Slot {
		a := time.Date(day.Year(), day.Month(), day.Day(), 0, m, 0, 0, loc)
		b := a.Add(time.Duration(mins) * time.Minute)
		if a.Before(now) || r.InBreak(m, m+mins) || slices.ContainsFunc(busy, func(x span) bool { return x.a.Before(b) && x.b.After(a) }) {
			continue
		}
		free = append(free, a)
	}
	return free, nil
}

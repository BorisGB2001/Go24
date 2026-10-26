package web

import (
	"fmt"
	"net/http"
	"slices"
	"time"

	"go24/internal/apikeys"
	"go24/internal/booking"
	"go24/internal/i18n"
)

// ---------- REST of «Онлайн-запись» (booking.v1.*) ----------
//
// booking.v1.resourceType.*  — /api/v1/booking/types
// booking.v1.resource.*      — /api/v1/booking/resources (resource.slots.list — /resources/{id}/slots)
// booking.v1.booking.*       — /api/v1/booking/bookings (client.set, externalData.set — the client and CRM fields)
// booking.v1.waitlist.*      — /api/v1/booking/waitlist (waitlist.createfrombooking — /bookings/{id}/wait)
// The types and resources are changed by administrators; bookings and the waiting list — by any employee, as on the page.

func (s *Server) apiBookingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/booking/types", s.api("booking:read", s.apiBkTypes))
	mux.HandleFunc("POST /api/v1/booking/types", s.api("booking:write", s.apiBkTypeSave))
	mux.HandleFunc("PUT /api/v1/booking/types/{id}", s.api("booking:write", s.apiBkTypeSave))
	mux.HandleFunc("DELETE /api/v1/booking/types/{id}", s.api("booking:write", s.apiBkTypeDelete))
	mux.HandleFunc("GET /api/v1/booking/resources", s.api("booking:read", s.apiBkResources))
	mux.HandleFunc("GET /api/v1/booking/resources/{id}", s.api("booking:read", s.apiBkResource))
	mux.HandleFunc("GET /api/v1/booking/resources/{id}/slots", s.api("booking:read", s.apiBkSlots))
	mux.HandleFunc("POST /api/v1/booking/resources", s.api("booking:write", s.apiBkResourceSave))
	mux.HandleFunc("PUT /api/v1/booking/resources/{id}", s.api("booking:write", s.apiBkResourceSave))
	mux.HandleFunc("DELETE /api/v1/booking/resources/{id}", s.api("booking:write", s.apiBkResourceDelete))
	mux.HandleFunc("GET /api/v1/booking/bookings", s.api("booking:read", s.apiBkBookings))
	mux.HandleFunc("GET /api/v1/booking/bookings/{id}", s.api("booking:read", s.apiBkBooking))
	mux.HandleFunc("POST /api/v1/booking/bookings", s.api("booking:write", s.apiBkBookingSave))
	mux.HandleFunc("PUT /api/v1/booking/bookings/{id}", s.api("booking:write", s.apiBkBookingSave))
	mux.HandleFunc("POST /api/v1/booking/bookings/{id}/status", s.api("booking:write", s.apiBkStatus))
	mux.HandleFunc("POST /api/v1/booking/bookings/{id}/wait", s.api("booking:write", s.apiBkToWait))
	mux.HandleFunc("DELETE /api/v1/booking/bookings/{id}", s.api("booking:write", s.apiBkBookingDelete))
	mux.HandleFunc("GET /api/v1/booking/waitlist", s.api("booking:read", s.apiBkWaitlist))
	mux.HandleFunc("GET /api/v1/booking/waitlist/{id}", s.api("booking:read", s.apiBkWait))
	mux.HandleFunc("POST /api/v1/booking/waitlist", s.api("booking:write", s.apiBkWaitSave))
	mux.HandleFunc("PUT /api/v1/booking/waitlist/{id}", s.api("booking:write", s.apiBkWaitSave))
	mux.HandleFunc("POST /api/v1/booking/waitlist/{id}/book", s.api("booking:write", s.apiBkWaitBook))
	mux.HandleFunc("DELETE /api/v1/booking/waitlist/{id}", s.api("booking:write", s.apiBkWaitDelete))
	mux.HandleFunc("GET /api/v1/booking/stats", s.api("booking:read", s.apiBkStats))
}

// apiBkAdmin: the key's owner is an administrator.
func (s *Server) apiBkAdmin(r *http.Request, p apikeys.Principal) error {
	if !s.apiAdmin(r.Context(), p.UserID) {
		return i18n.E("err.forbidden")
	}
	return nil
}

type apiBkType struct {
	ID        string `json:"id"`
	Code      string `json:"code,omitempty"` // a standard type: employee, room, equipment
	Name      string `json:"name"`
	Resources int    `json:"resources"`
}

func (s *Server) apiBkTypeName(r *http.Request, code, name string) string {
	if code != "" {
		return s.apiTr(r)("booking.type." + code)
	}
	return name
}

// GET /api/v1/booking/types — the resource types.
func (s *Server) apiBkTypes(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Booking.Types(r.Context())
	if err != nil {
		return err
	}
	out := make([]apiBkType, 0, len(list))
	for _, x := range list {
		out = append(out, apiBkType{x.ID, x.Code, s.apiBkTypeName(r, x.Code, x.Name), x.Resources})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// POST /api/v1/booking/types, PUT /api/v1/booking/types/{id} — {"name"}: a type of its own; the standard ones keep
// their names.
func (s *Server) apiBkTypeSave(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiBkAdmin(r, p); err != nil {
		return err
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	id, err := s.Booking.SaveType(r.Context(), r.PathValue("id"), in.Name)
	if err != nil {
		return err
	}
	writeJSON(w, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[r.PathValue("id") == ""], map[string]string{"id": id})
	return nil
}

func (s *Server) apiBkTypeDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiBkAdmin(r, p); err != nil {
		return err
	}
	if err := s.Booking.DeleteType(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type apiBkResource struct {
	ID          string         `json:"id"`
	TypeID      string         `json:"type_id"`
	TypeCode    string         `json:"type_code,omitempty"`
	TypeName    string         `json:"type_name"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	UserID      string         `json:"user_id,omitempty"` // the employee the resource is
	UserName    string         `json:"user_name,omitempty"`
	Slot        int            `json:"slot_minutes"`
	Days        []int          `json:"days"` // ISO weekdays, 1 — Monday
	From        string         `json:"from"` // "09:00"
	To          string         `json:"to"`   // "18:00"
	NotifyMail  bool           `json:"notify_mail"`
	RemindHours int            `json:"remind_hours"`
	LateMinutes int            `json:"late_minutes"`
	Overbooking bool           `json:"overbooking"`
	Services    []apiBkService `json:"services"` // the catalog's services it provides
}

// apiBkService is a service of a resource (its minutes) or of a booking (as booked) — the sku of booking.v1.
type apiBkService struct {
	ProductID string  `json:"product_id"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	Currency  string  `json:"currency"`
	Minutes   int     `json:"minutes"`
}

func toAPIBkServices(list []booking.Service) []apiBkService {
	out := make([]apiBkService, 0, len(list))
	for _, x := range list {
		out = append(out, apiBkService{x.ProductID, x.Name, x.Price, x.Currency, x.Minutes})
	}
	return out
}

func (s *Server) toAPIBkResource(r *http.Request, x booking.Resource) apiBkResource {
	return apiBkResource{x.ID, x.TypeID, x.TypeCode, s.apiBkTypeName(r, x.TypeCode, x.TypeName), x.Name, x.Description,
		x.UserID, x.UserName, x.Slot, x.Days, hhmm(x.From), hhmm(x.To), x.NotifyMail, x.RemindHours, x.LateMinutes, x.Overbooking,
		toAPIBkServices(x.Services)}
}

// orEmpty: nil as an empty list in JSON.
func orEmpty[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

// hhmm: minutes of the day as «09:30»; parseHHMM back (-1 — not a time).
func hhmm(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }

func parseHHMM(v string) int {
	var h, m int
	if _, err := fmt.Sscanf(v, "%d:%d", &h, &m); err != nil || h < 0 || m < 0 || m > 59 {
		return -1
	}
	return h*60 + m
}

// GET /api/v1/booking/resources?type_id= — the resources.
func (s *Server) apiBkResources(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Booking.Resources(r.Context(), r.URL.Query().Get("type_id"))
	if err != nil {
		return err
	}
	out := make([]apiBkResource, 0, len(list))
	for _, x := range list {
		out = append(out, s.toAPIBkResource(r, x))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiBkResource(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	x, err := s.Booking.Resource(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.toAPIBkResource(r, x))
	return nil
}

// GET /api/v1/booking/resources/{id}/slots?date=2026-10-27&service_id= — the free slots of the resource that day
// (today by default): their starts; each is slot_minutes long, or — for a service of the resource — the starts from
// which its minutes are free.
func (s *Server) apiBkSlots(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	x, err := s.Booking.Resource(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	day := time.Now()
	if v := r.URL.Query().Get("date"); v != "" {
		if day, err = time.ParseInLocation("2006-01-02", v, time.Local); err != nil {
			return booking.ErrTime
		}
	}
	mins := x.Slot
	if id := r.URL.Query().Get("service_id"); id != "" {
		svc, ok := x.Service(id)
		if !ok {
			return booking.ErrService
		}
		mins = svc.Minutes
	}
	free, err := s.Booking.FreeFor(r.Context(), x, day, time.Local, mins)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"resource_id": x.ID, "slot_minutes": x.Slot, "minutes": mins, "free": orEmpty(free)})
	return nil
}

// POST /api/v1/booking/resources, PUT /api/v1/booking/resources/{id} — a resource (administrators): the fields of
// GET; on PUT those not given stay. A new one works Mon–Fri 9:00–18:00 by 30 minutes unless told otherwise.
// "services": [{"product_id", "minutes"}] — the catalog's services (of the type «Услуга»), minutes 0 — a slot.
func (s *Server) apiBkResourceSave(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiBkAdmin(r, p); err != nil {
		return err
	}
	var in struct {
		TypeID      *string `json:"type_id"`
		Name        *string `json:"name"`
		Description *string `json:"description"`
		UserID      *string `json:"user_id"`
		Slot        *int    `json:"slot_minutes"`
		Days        []int   `json:"days"`
		From        *string `json:"from"`
		To          *string `json:"to"`
		NotifyMail  *bool   `json:"notify_mail"`
		RemindHours *int    `json:"remind_hours"`
		LateMinutes *int    `json:"late_minutes"`
		Overbooking *bool   `json:"overbooking"`
		Services    []struct {
			ProductID string `json:"product_id"`
			Minutes   int    `json:"minutes"`
		} `json:"services"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	x := booking.Resource{Slot: 30, Days: []int{1, 2, 3, 4, 5}, From: 9 * 60, To: 18 * 60, NotifyMail: true, RemindHours: 24, LateMinutes: 15}
	if id := r.PathValue("id"); id != "" {
		var err error
		if x, err = s.Booking.Resource(r.Context(), id); err != nil {
			return err
		}
	} else if in.TypeID == nil {
		types, err := s.Booking.Types(r.Context())
		if err != nil {
			return err
		}
		if len(types) > 0 {
			x.TypeID = types[0].ID
		}
	}
	x.TypeID, x.Name, x.Description = deref(in.TypeID, x.TypeID), deref(in.Name, x.Name), deref(in.Description, x.Description)
	x.UserID, x.Slot = deref(in.UserID, x.UserID), deref(in.Slot, x.Slot)
	if in.Days != nil {
		x.Days = in.Days
	}
	if in.From != nil {
		x.From = parseHHMM(*in.From)
	}
	if in.To != nil {
		if x.To = parseHHMM(*in.To); x.To == 0 { // «до 24:00» as 00:00
			x.To = 24 * 60
		}
	}
	x.NotifyMail, x.RemindHours = deref(in.NotifyMail, x.NotifyMail), deref(in.RemindHours, x.RemindHours)
	x.LateMinutes, x.Overbooking = deref(in.LateMinutes, x.LateMinutes), deref(in.Overbooking, x.Overbooking)
	if in.Services != nil {
		x.Services = []booking.Service{}
		for _, v := range in.Services {
			x.Services = append(x.Services, booking.Service{ProductID: v.ProductID, Minutes: v.Minutes})
		}
	}
	id, err := s.Booking.SaveResource(r.Context(), p.UserID, x)
	if err != nil {
		return err
	}
	if x, err = s.Booking.Resource(r.Context(), id); err != nil {
		return err
	}
	writeJSON(w, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[r.PathValue("id") == ""], s.toAPIBkResource(r, x))
	return nil
}

// DELETE /api/v1/booking/resources/{id} — the resource with its bookings (administrators).
func (s *Server) apiBkResourceDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiBkAdmin(r, p); err != nil {
		return err
	}
	if err := s.Booking.DeleteResource(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type apiBkBooking struct {
	ID          string         `json:"id"`
	ResourceID  string         `json:"resource_id"`
	Resource    string         `json:"resource_name"`
	Extras      []string       `json:"extra_resource_ids"` // the other resources it takes at the same time
	Start       time.Time      `json:"start"`
	End         time.Time      `json:"end"`
	ClientName  string         `json:"client_name"`
	ClientPhone string         `json:"client_phone"`
	ClientEmail string         `json:"client_email"`
	Note        string         `json:"note"`
	Status      string         `json:"status"` // unconfirmed, confirmed, came, no_show, cancelled
	ContactID   string         `json:"contact_id,omitempty"`
	CompanyID   string         `json:"company_id,omitempty"`
	DealID      string         `json:"deal_id,omitempty"`
	CreatedBy   string         `json:"created_by,omitempty"`
	LinkID      string         `json:"link_id,omitempty"` // booked by the client by this link
	ClientURL   string         `json:"client_url"`        // the client's page: confirm or cancel
	Services    []apiBkService `json:"services"`          // as booked
	Amount      float64        `json:"amount"`            // of the services
	Currency    string         `json:"currency,omitempty"`
}

func (s *Server) toAPIBkBooking(b booking.Booking) apiBkBooking {
	u := ""
	if b.ClientToken != "" {
		u = s.BaseURL + "/bk/b/" + b.ClientToken
	}
	sum, cur := b.Total()
	return apiBkBooking{b.ID, b.ResourceID, b.ResourceName, orEmpty(b.Extras), b.Start, b.End, b.ClientName,
		b.ClientPhone, b.ClientEmail, b.Note, b.Status, b.ContactID, b.CompanyID, b.DealID, b.CreatedBy, b.LinkID, u,
		toAPIBkServices(b.Services), sum, cur}
}

// GET /api/v1/booking/bookings?from=2026-10-01&to=2026-10-31&resource_id=&status= — the bookings that touch the days
// (both in; the current month by default, at most a year), by time; resource_id — those that take the resource.
func (s *Server) apiBkBookings(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	from, to := bookingPeriod(r)
	list, err := s.Booking.Bookings(r.Context(), from, to)
	if err != nil {
		return err
	}
	res, status := r.URL.Query().Get("resource_id"), r.URL.Query().Get("status")
	out := []apiBkBooking{}
	for _, b := range list {
		if (res == "" || b.Takes(res)) && (status == "" || b.Status == status) {
			out = append(out, s.toAPIBkBooking(b))
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiBkBooking(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	b, err := s.Booking.Booking(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.toAPIBkBooking(b))
	return nil
}

// POST /api/v1/booking/bookings, PUT /api/v1/booking/bookings/{id} — a booking: {"resource_id", "extra_resource_ids",
// "service_ids", "start", "end" or "minutes" (the services' minutes together, else a slot of the resource by default),
// "client_name", "client_phone", "client_email", "note", "status", "contact_id", "company_id", "deal_id"}; on PUT
// those not given stay, a service the booking had keeps its price. Its time must be free on each
// resource (409 otherwise) unless the resource allows overbooking; the CRM cards must be readable by the key's owner,
// a deal brings its contact and company, a contact — the client's name and phone when they are not given.
func (s *Server) apiBkBookingSave(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		ResourceID  *string    `json:"resource_id"`
		Extras      []string   `json:"extra_resource_ids"`
		Start       *time.Time `json:"start"`
		End         *time.Time `json:"end"`
		Minutes     *int       `json:"minutes"`
		ClientName  *string    `json:"client_name"`
		ClientPhone *string    `json:"client_phone"`
		ClientEmail *string    `json:"client_email"`
		Note        *string    `json:"note"`
		Status      *string    `json:"status"`
		ContactID   *string    `json:"contact_id"`
		CompanyID   *string    `json:"company_id"`
		DealID      *string    `json:"deal_id"`
		ServiceIDs  []string   `json:"service_ids"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	var b, old booking.Booking
	if id := r.PathValue("id"); id != "" {
		var err error
		if old, err = s.Booking.Booking(r.Context(), id); err != nil {
			return err
		}
		b = old
	}
	if in.Status != nil && !slices.Contains(booking.Statuses, *in.Status) {
		return i18n.E("booking.err.status")
	}
	b.ResourceID = deref(in.ResourceID, b.ResourceID)
	if in.Extras != nil {
		b.Extras = in.Extras
	}
	if in.ServiceIDs != nil {
		b.Services = booking.Keep(in.ServiceIDs, old.Services)
	}
	minutes := int(b.End.Sub(b.Start).Minutes())
	if in.Start == nil && b.ID == "" {
		return booking.ErrTime
	}
	if in.Start != nil {
		b.Start = *in.Start
	}
	switch {
	case in.End != nil:
		b.End = *in.End
	case in.Minutes != nil:
		b.End = b.Start.Add(time.Duration(*in.Minutes) * time.Minute)
	case b.ID == "":
		x, err := s.Booking.Resource(r.Context(), b.ResourceID)
		if err != nil {
			return err
		}
		mins := x.Slot
		if n := 0; len(b.Services) > 0 {
			for _, v := range b.Services {
				svc, ok := x.Service(v.ProductID)
				if !ok {
					return booking.ErrService
				}
				n += svc.Minutes
			}
			mins = n
		}
		b.End = b.Start.Add(time.Duration(mins) * time.Minute)
	default: // moved: the same length
		b.End = b.Start.Add(time.Duration(minutes) * time.Minute)
	}
	b.ClientName, b.ClientPhone = deref(in.ClientName, b.ClientName), deref(in.ClientPhone, b.ClientPhone)
	b.ClientEmail, b.Note, b.Status = deref(in.ClientEmail, b.ClientEmail), deref(in.Note, b.Note), deref(in.Status, b.Status)
	b.ContactID, b.CompanyID, b.DealID = deref(in.ContactID, b.ContactID), deref(in.CompanyID, b.CompanyID), deref(in.DealID, b.DealID)
	if err := s.bookingCards(r, s.apiSess(r, p), &b); err != nil {
		return err
	}
	id, err := s.Booking.SaveBooking(r.Context(), p.UserID, b)
	if err != nil {
		return err
	}
	if b, err = s.Booking.Booking(r.Context(), id); err != nil {
		return err
	}
	s.bookingEvents(r.Context(), p.UserID, old, b)
	writeJSON(w, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[old.ID == ""], s.toAPIBkBooking(b))
	return nil
}

// POST /api/v1/booking/bookings/{id}/status — {"status"}: confirmed, came, no_show, cancelled…; one brought back from
// cancelled must find its time free.
func (s *Server) apiBkStatus(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Status string `json:"status"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if !slices.Contains(booking.Statuses, in.Status) {
		return i18n.E("booking.err.status")
	}
	if err := s.bookingSetStatus(r.Context(), p.UserID, r.PathValue("id"), in.Status); err != nil {
		return err
	}
	return s.apiBkBooking(w, r, p)
}

// POST /api/v1/booking/bookings/{id}/wait — «В лист ожидания»: the client goes back on the waiting list, the booking
// is removed; the entry.
func (s *Server) apiBkToWait(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id, err := s.Booking.ToWait(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	x, err := s.Booking.Wait(r.Context(), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, toAPIBkWait(x))
	return nil
}

func (s *Server) apiBkBookingDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.Booking.DeleteBooking(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type apiBkWait struct {
	ID          string    `json:"id"`
	ResourceID  string    `json:"resource_id,omitempty"` // the resource the client wants; none — any
	Resource    string    `json:"resource_name,omitempty"`
	ClientName  string    `json:"client_name"`
	ClientPhone string    `json:"client_phone"`
	ClientEmail string    `json:"client_email"`
	Note        string    `json:"note"`
	ContactID   string    `json:"contact_id,omitempty"`
	CompanyID   string    `json:"company_id,omitempty"`
	DealID      string    `json:"deal_id,omitempty"`
	CreatedBy   string    `json:"created_by,omitempty"`
	Created     time.Time `json:"created_at"`
}

func toAPIBkWait(x booking.Wait) apiBkWait {
	return apiBkWait{x.ID, x.ResourceID, x.ResourceName, x.ClientName, x.ClientPhone, x.ClientEmail, x.Note, x.ContactID,
		x.CompanyID, x.DealID, x.CreatedBy, x.Created}
}

// GET /api/v1/booking/waitlist — the waiting list, the earliest first.
func (s *Server) apiBkWaitlist(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Booking.Waitlist(r.Context())
	if err != nil {
		return err
	}
	out := make([]apiBkWait, 0, len(list))
	for _, x := range list {
		out = append(out, toAPIBkWait(x))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiBkWait(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	x, err := s.Booking.Wait(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toAPIBkWait(x))
	return nil
}

// POST /api/v1/booking/waitlist, PUT /api/v1/booking/waitlist/{id} — {"resource_id", "client_name", "client_phone",
// "client_email", "note", "contact_id", "company_id", "deal_id"}; on PUT those not given stay.
func (s *Server) apiBkWaitSave(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		ResourceID  *string `json:"resource_id"`
		ClientName  *string `json:"client_name"`
		ClientPhone *string `json:"client_phone"`
		ClientEmail *string `json:"client_email"`
		Note        *string `json:"note"`
		ContactID   *string `json:"contact_id"`
		CompanyID   *string `json:"company_id"`
		DealID      *string `json:"deal_id"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	var x booking.Wait
	if id := r.PathValue("id"); id != "" {
		var err error
		if x, err = s.Booking.Wait(r.Context(), id); err != nil {
			return err
		}
	}
	x.ResourceID, x.ClientName, x.ClientPhone = deref(in.ResourceID, x.ResourceID), deref(in.ClientName, x.ClientName), deref(in.ClientPhone, x.ClientPhone)
	x.ClientEmail, x.Note = deref(in.ClientEmail, x.ClientEmail), deref(in.Note, x.Note)
	if x.ResourceID != "" {
		if _, err := s.Booking.Resource(r.Context(), x.ResourceID); err != nil {
			return err
		}
	}
	// the cards as those of a booking
	b := booking.Booking{ClientName: x.ClientName, ClientPhone: x.ClientPhone, ClientEmail: x.ClientEmail,
		ContactID: deref(in.ContactID, x.ContactID), CompanyID: deref(in.CompanyID, x.CompanyID), DealID: deref(in.DealID, x.DealID)}
	if err := s.bookingCards(r, s.apiSess(r, p), &b); err != nil {
		return err
	}
	x.ClientName, x.ClientPhone, x.ClientEmail = b.ClientName, b.ClientPhone, b.ClientEmail
	x.ContactID, x.CompanyID, x.DealID = b.ContactID, b.CompanyID, b.DealID
	id, err := s.Booking.SaveWait(r.Context(), p.UserID, x)
	if err != nil {
		return err
	}
	if x, err = s.Booking.Wait(r.Context(), id); err != nil {
		return err
	}
	writeJSON(w, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[r.PathValue("id") == ""], toAPIBkWait(x))
	return nil
}

// POST /api/v1/booking/waitlist/{id}/book — {"resource_id", "start"}: the client is booked for a slot of the resource
// and taken off the list; the booking.
func (s *Server) apiBkWaitBook(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		ResourceID string    `json:"resource_id"`
		Start      time.Time `json:"start"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Start.IsZero() {
		return booking.ErrTime
	}
	b, err := s.Booking.BookWait(r.Context(), p.UserID, r.PathValue("id"), in.ResourceID, in.Start)
	if err != nil {
		return err
	}
	s.bookingEvents(r.Context(), p.UserID, booking.Booking{}, b)
	if b, err = s.Booking.Booking(r.Context(), b.ID); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, s.toAPIBkBooking(b))
	return nil
}

func (s *Server) apiBkWaitDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.Booking.DeleteWait(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type apiBkStat struct {
	ResourceID  string         `json:"resource_id,omitempty"`
	Name        string         `json:"name,omitempty"`
	Work        int            `json:"work_minutes"`
	Busy        int            `json:"busy_minutes"`
	Load        int            `json:"load"` // percent
	Bookings    int            `json:"bookings"`
	Statuses    map[string]int `json:"statuses"`
	NoShowShare *int           `json:"no_show_share"` // percent of no_show among came and no_show; null — none of them
}

func toAPIBkStat(x booking.Stat) apiBkStat {
	o := apiBkStat{x.Resource.ID, x.Resource.Name, x.Work, x.Busy, x.Load, x.Bookings, x.Statuses, nil}
	if x.NoShowShare >= 0 {
		o.NoShowShare = &x.NoShowShare
	}
	return o
}

// GET /api/v1/booking/stats?from=&to=&type_id= — the load of the resources for the days (as the page «Статистика»).
func (s *Server) apiBkStats(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	from, to := bookingPeriod(r)
	list, total, err := s.Booking.Stats(r.Context(), r.URL.Query().Get("type_id"), from, to, time.Local)
	if err != nil {
		return err
	}
	out := make([]apiBkStat, 0, len(list))
	for _, x := range list {
		out = append(out, toAPIBkStat(x))
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from.Format("2006-01-02"), "to": to.AddDate(0, 0, -1).Format("2006-01-02"),
		"resources": out, "total": toAPIBkStat(total)})
	return nil
}

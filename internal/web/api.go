package web

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/apps"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// REST API v1. Auth: "Authorization: Bearer go24_<key_id>_<secret>", rights limited by key scopes.

func (s *Server) apiRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/me", s.api("", s.apiMe))
	mux.HandleFunc("GET /api/v1/projects", s.api("projects:read", s.apiProjects))
	mux.HandleFunc("POST /api/v1/projects", s.api("projects:write", s.apiCreateProject))
	mux.HandleFunc("GET /api/v1/projects/{key}", s.api("projects:read", s.apiProject))
	mux.HandleFunc("GET /api/v1/projects/{key}/tasks", s.api("tasks:read", s.apiProjectTasks))
	mux.HandleFunc("POST /api/v1/projects/{key}/tasks", s.api("tasks:write", s.apiCreateTask))
	mux.HandleFunc("GET /api/v1/tasks", s.api("tasks:read", s.apiMyTasks))
	mux.HandleFunc("GET /api/v1/tasks/{key}", s.api("tasks:read", s.apiTask))
	mux.HandleFunc("GET /api/v1/tasks/{key}/views", s.api("tasks:read", s.apiTaskViews))
	mux.HandleFunc("PATCH /api/v1/tasks/{key}", s.api("tasks:write", s.apiUpdateTask))
	mux.HandleFunc("DELETE /api/v1/tasks/{key}", s.api("tasks:write", s.apiDeleteTask))
	mux.HandleFunc("GET /api/v1/trash", s.api("tasks:read", s.apiTrash))
	mux.HandleFunc("GET /api/v1/efficiency", s.api("tasks:read", s.apiEfficiency))
	mux.HandleFunc("GET /api/v1/mail/boxes", s.api("mail:read", s.apiMailBoxes))
	mux.HandleFunc("POST /api/v1/mail/boxes", s.api("mail:write", s.apiMailConnect))
	mux.HandleFunc("POST /api/v1/mail/boxes/{id}/sync", s.api("mail:read", s.apiMailSync))
	mux.HandleFunc("POST /api/v1/mail/boxes/{id}/folders", s.api("mail:write", s.apiMailFolderAdd))
	mux.HandleFunc("POST /api/v1/mail/folders/{id}/rename", s.api("mail:write", s.apiMailFolderRename))
	mux.HandleFunc("DELETE /api/v1/mail/folders/{id}", s.api("mail:write", s.apiMailFolderDelete))
	mux.HandleFunc("GET /api/v1/mail/messages", s.api("mail:read", s.apiMailMessages))
	mux.HandleFunc("GET /api/v1/mail/messages/{id}", s.api("mail:read", s.apiMailMessage))
	mux.HandleFunc("POST /api/v1/mail/move", s.api("mail:write", s.apiMailMove))
	mux.HandleFunc("GET /api/v1/mail/messages/{id}/conversation", s.api("mail:read", s.apiMailConversation))
	mux.HandleFunc("POST /api/v1/mail/act", s.api("mail:write", s.apiMailAct))
	mux.HandleFunc("GET /api/v1/mail/categories", s.api("mail:read", s.apiMailCategories))
	mux.HandleFunc("GET /api/v1/mail/searches", s.api("mail:read", s.apiMailSearches))
	mux.HandleFunc("POST /api/v1/mail/searches", s.api("mail:write", s.apiMailSearchSave))
	mux.HandleFunc("DELETE /api/v1/mail/searches/{id}", s.api("mail:write", s.apiMailSearchDelete))
	mux.HandleFunc("GET /api/v1/mail/lists", s.api("mail:read", s.apiMailLists))
	mux.HandleFunc("POST /api/v1/mail/lists", s.api("mail:write", s.apiMailListSave))
	mux.HandleFunc("POST /api/v1/mail/lists/{id}", s.api("mail:write", s.apiMailListSave))
	mux.HandleFunc("DELETE /api/v1/mail/lists/{id}", s.api("mail:write", s.apiMailListDelete))
	mux.HandleFunc("GET /api/v1/mail/rules", s.api("mail:read", s.apiMailRules))
	mux.HandleFunc("POST /api/v1/mail/rules", s.api("mail:write", s.apiMailRuleSave))
	mux.HandleFunc("POST /api/v1/mail/rules/run", s.api("mail:write", s.apiMailRulesRun))
	mux.HandleFunc("POST /api/v1/mail/rules/{id}", s.api("mail:write", s.apiMailRuleSave))
	mux.HandleFunc("DELETE /api/v1/mail/rules/{id}", s.api("mail:write", s.apiMailRuleDelete))
	mux.HandleFunc("GET /api/v1/mail/ooo", s.api("mail:read", s.apiMailOOO))
	mux.HandleFunc("POST /api/v1/mail/ooo", s.api("mail:write", s.apiMailOOO))
	mux.HandleFunc("POST /api/v1/mail/sweep", s.api("mail:write", s.apiMailSweep))
	mux.HandleFunc("GET /api/v1/mail/sweeps", s.api("mail:read", s.apiMailSweeps))
	mux.HandleFunc("DELETE /api/v1/mail/sweeps/{id}", s.api("mail:write", s.apiMailSweepDelete))
	mux.HandleFunc("GET /api/v1/mail/senders", s.api("mail:read", s.apiMailSenders))
	mux.HandleFunc("POST /api/v1/mail/senders", s.api("mail:write", s.apiMailSenderAdd))
	mux.HandleFunc("DELETE /api/v1/mail/senders/{sender}", s.api("mail:write", s.apiMailSenderDrop))
	mux.HandleFunc("GET /api/v1/mail/notify", s.api("mail:read", s.apiMailNotify))
	mux.HandleFunc("POST /api/v1/mail/notify", s.api("mail:write", s.apiMailNotify))
	mux.HandleFunc("GET /api/v1/mail/messages/{id}/links", s.api("mail:read", s.apiMailLinks))
	mux.HandleFunc("POST /api/v1/mail/messages/{id}/invite", s.api("mail:write", s.apiMailInvite))
	mux.HandleFunc("POST /api/v1/mail/messages/{id}/crm", s.api("mail:write", s.apiMailCRM))
	mux.HandleFunc("GET /api/v1/mail/messages/{id}/votes", s.api("mail:read", s.apiMailVotes))
	mux.HandleFunc("POST /api/v1/mail/messages/{id}/vote", s.api("mail:write", s.apiMailVote))
	mux.HandleFunc("GET /api/v1/mail/messages/{id}/recall", s.api("mail:read", s.apiMailRecalls))
	mux.HandleFunc("POST /api/v1/mail/messages/{id}/recall", s.api("mail:write", s.apiMailRecall))
	mux.HandleFunc("GET /api/v1/mail/shared", s.api("mail:read", s.apiMailShared))
	mux.HandleFunc("GET /api/v1/mail/shared/{box}/tickets", s.api("mail:read", s.apiMailTickets))
	mux.HandleFunc("POST /api/v1/mail/shared/{box}/letters/{letter}/{do}", s.api("mail:write", s.apiMailTicketDo))
	mux.HandleFunc("GET /api/v1/flows", s.api("tasks:read", s.apiFlows))
	mux.HandleFunc("POST /api/v1/flows", s.api("tasks:write", s.apiFlowCreate))
	mux.HandleFunc("POST /api/v1/flows/{id}/tasks", s.api("tasks:write", s.apiFlowTask))
	mux.HandleFunc("POST /api/v1/tasks/{key}/take", s.api("tasks:write", s.apiTake))
	mux.HandleFunc("POST /api/v1/trash/{id}/restore", s.api("tasks:write", s.apiTrashRestore))
	mux.HandleFunc("POST /api/v1/tasks/{key}/actions/{action}", s.api("tasks:write", s.apiAction))
	mux.HandleFunc("POST /api/v1/tasks/{key}/comments", s.api("tasks:write", s.apiComment))
	mux.HandleFunc("POST /api/v1/tasks/{key}/checklist", s.api("tasks:write", s.apiChecklistAdd))
	mux.HandleFunc("POST /api/v1/tasks/{key}/checklist/{id}/toggle", s.api("tasks:write", s.apiChecklistToggle))
	mux.HandleFunc("DELETE /api/v1/tasks/{key}/checklist/{id}", s.api("tasks:write", s.apiChecklistDelete))
	mux.HandleFunc("POST /api/v1/tasks/{key}/tags", s.api("tasks:write", s.apiTag))
	mux.HandleFunc("POST /api/v1/tasks/{key}/links", s.api("tasks:write", s.apiLinkAdd))
	mux.HandleFunc("DELETE /api/v1/tasks/{key}/links/{id}", s.api("tasks:write", s.apiLinkDelete))
	mux.HandleFunc("POST /api/v1/tasks/{key}/files", s.api("tasks:write", s.apiFileAdd))
	mux.HandleFunc("POST /api/v1/tasks/{key}/participants", s.api("tasks:write", s.apiParticipantAdd))
	mux.HandleFunc("DELETE /api/v1/tasks/{key}/participants/{role}/{uid}", s.api("tasks:write", s.apiParticipantRemove))
	mux.HandleFunc("POST /api/v1/tasks/{key}/time", s.api("tasks:write", s.apiTimeAdd))
	mux.HandleFunc("POST /api/v1/tasks/{key}/timer/start", s.api("tasks:write", s.apiTimerStart))
	mux.HandleFunc("POST /api/v1/timer/stop", s.api("tasks:write", s.apiTimerStop))
	// the person's inbox: notifications and the messenger (an employee working through the API, e.g. an assistant)
	mux.HandleFunc("GET /api/v1/notifications", s.api("im:read", s.apiNotifications))
	mux.HandleFunc("POST /api/v1/notifications/read", s.api("im:read", s.apiNotificationsRead))
	mux.HandleFunc("GET /api/v1/im/rooms", s.api("im:read", s.apiRooms))
	mux.HandleFunc("GET /api/v1/im/rooms/{room}/messages", s.api("im:read", s.apiRoomMessages))
	mux.HandleFunc("POST /api/v1/im/rooms/{room}/messages", s.api("im:write", s.apiRoomSend))
	mux.HandleFunc("PATCH /api/v1/im/rooms/{room}/messages/{id}", s.api("im:write", s.apiMessageEdit))
	mux.HandleFunc("DELETE /api/v1/im/rooms/{room}/messages/{id}", s.api("im:write", s.apiMessageDelete))
	mux.HandleFunc("PUT /api/v1/im/rooms/{room}/messages/{id}/reactions/{key}", s.api("im:write", s.apiReaction))
	mux.HandleFunc("DELETE /api/v1/im/rooms/{room}/messages/{id}/reactions/{key}", s.api("im:write", s.apiReaction))
	mux.HandleFunc("POST /api/v1/im/direct", s.api("im:write", s.apiDirect))
	mux.HandleFunc("POST /api/v1/im/notes", s.api("im:write", s.apiNotes))
	mux.HandleFunc("GET /api/v1/im/calls", s.api("im:read", s.apiCalls))
	mux.HandleFunc("GET /api/v1/settings/portal", s.api("settings:read", s.apiPortal))
	mux.HandleFunc("PUT /api/v1/settings/portal", s.api("settings:write", s.apiPortal))
	mux.HandleFunc("GET /api/v1/settings/events", s.api("settings:read", s.apiAudit))
	mux.HandleFunc("GET /api/v1/settings/admins", s.api("settings:read", s.apiAdmins))
	mux.HandleFunc("POST /api/v1/settings/admins", s.api("settings:write", s.apiAdmins))
	mux.HandleFunc("DELETE /api/v1/settings/admins/{id}", s.api("settings:write", s.apiAdmins))
	mux.HandleFunc("GET /api/v1/settings/ip-access", s.api("settings:read", s.apiIPAccess))
	mux.HandleFunc("GET /api/v1/settings/backup", s.api("settings:read", s.apiBackup))
	mux.HandleFunc("POST /api/v1/settings/backup/run", s.api("settings:write", s.apiBackup))
	mux.HandleFunc("PUT /api/v1/settings/ip-access", s.api("settings:write", s.apiIPAccess))
	mux.HandleFunc("GET /api/v1/im/settings", s.api("im:read", s.apiIMSettings))
	mux.HandleFunc("PUT /api/v1/im/settings", s.api("im:write", s.apiIMSettings))
	mux.HandleFunc("POST /api/v1/im/groups", s.api("im:write", s.apiGroup))
	mux.HandleFunc("GET /api/v1/im/rooms/{room}/members", s.api("im:read", s.apiGroupMembers))
	mux.HandleFunc("POST /api/v1/im/rooms/{room}/members", s.api("im:write", s.apiGroupAdd))
	mux.HandleFunc("DELETE /api/v1/im/rooms/{room}/members/{user}", s.api("im:write", s.apiGroupRemove))
	mux.HandleFunc("PATCH /api/v1/im/rooms/{room}", s.api("im:write", s.apiGroupRename))
	mux.HandleFunc("GET /api/v1/im/search", s.api("im:read", s.apiSearch))
	mux.HandleFunc("POST /api/v1/im/rooms/{room}/marks", s.api("im:write", s.apiRoomMark))
	mux.HandleFunc("GET /api/v1/im/rooms/{room}/pins", s.api("im:read", s.apiPins))
	mux.HandleFunc("PUT /api/v1/im/rooms/{room}/pins/{id}", s.api("im:write", s.apiPin))
	mux.HandleFunc("DELETE /api/v1/im/rooms/{room}/pins/{id}", s.api("im:write", s.apiPin))
	mux.HandleFunc("POST /api/v1/im/rooms/{room}/polls", s.api("im:write", s.apiPoll))
	mux.HandleFunc("GET /api/v1/im/rooms/{room}/history", s.api("im:read", s.apiHistory))
	mux.HandleFunc("GET /api/v1/im/rooms/{room}/files", s.api("im:read", s.apiChatMedia))
	mux.HandleFunc("POST /api/v1/im/rooms/{room}/polls/{id}/{action}", s.api("im:write", s.apiPollAction))
	mux.HandleFunc("POST /api/v1/im/rooms/{room}/files", s.api("im:write", s.apiRoomFile))
	mux.HandleFunc("GET /api/v1/im/rooms/{room}/messages/{id}/file", s.api("im:read", s.apiMessageFile))
	mux.HandleFunc("POST /api/v1/im/rooms/{room}/messages/{id}/transcript", s.api("im:read", s.apiTranscript))
	mux.HandleFunc("GET /api/v1/crm/pipelines", s.api("crm:read", s.apiCRMPipelines))
	mux.HandleFunc("GET /api/v1/crm/currencies", s.api("crm:read", s.apiCRMCurrencies))
	mux.HandleFunc("GET /api/v1/crm/recycle", s.api("crm:read", s.apiCRMRecycle))
	mux.HandleFunc("POST /api/v1/crm/recycle/{id}/restore", s.api("crm:write", s.apiCRMRestore))
	mux.HandleFunc("DELETE /api/v1/crm/recycle/{id}", s.api("crm:write", s.apiCRMPurge))
	mux.HandleFunc("GET /api/v1/crm/forms", s.api("crm:read", s.apiCRMForms))
	mux.HandleFunc("GET /api/v1/crm/forms/{id}/results", s.api("crm:read", s.apiCRMFormResults))
	mux.HandleFunc("GET /api/v1/crm/products", s.api("crm:read", s.apiCRMCatalog))
	mux.HandleFunc("GET /api/v1/crm/product-sections", s.api("crm:read", s.apiCRMSections))
	mux.HandleFunc("GET /api/v1/crm/product-units", s.api("crm:read", s.apiCRMUnits))
	mux.HandleFunc("GET /api/v1/crm/product-props", s.api("crm:read", s.apiCRMProps))
	// «Складской учёт» (api_stock.go)
	mux.HandleFunc("GET /api/v1/store/warehouses", s.api("crm:read", s.apiStockWarehouses))
	mux.HandleFunc("GET /api/v1/store/stock", s.api("crm:read", s.apiStock))
	mux.HandleFunc("GET /api/v1/store/reports/stock", s.api("crm:read", s.apiStockReport))
	mux.HandleFunc("GET /api/v1/store/reports/moves", s.api("crm:read", s.apiStockMoves))
	mux.HandleFunc("GET /api/v1/store/docs", s.api("crm:read", s.apiStockDocs))
	mux.HandleFunc("GET /api/v1/store/docs/{id}", s.api("crm:read", s.apiStockDoc))
	mux.HandleFunc("POST /api/v1/store/docs", s.api("crm:write", s.apiStockDocSave))
	mux.HandleFunc("PUT /api/v1/store/docs/{id}", s.api("crm:write", s.apiStockDocSave))
	mux.HandleFunc("DELETE /api/v1/store/docs/{id}", s.api("crm:write", s.apiStockDocDelete))
	mux.HandleFunc("POST /api/v1/store/docs/{id}/{op}", s.api("crm:write", s.apiStockDocMove))
	mux.HandleFunc("POST /api/v1/crm/deals/{id}/shipment", s.api("crm:write", s.apiDealShipment))
	mux.HandleFunc("GET /api/v1/crm/{kind}", s.api("crm:read", s.apiCRMList))
	mux.HandleFunc("POST /api/v1/crm/{kind}", s.api("crm:write", s.apiCRMCreate))
	mux.HandleFunc("GET /api/v1/crm/{kind}/{id}", s.api("crm:read", s.apiCRMGet))
	mux.HandleFunc("POST /api/v1/crm/{kind}/{id}/stage", s.api("crm:write", s.apiCRMStage))
	mux.HandleFunc("PUT /api/v1/crm/{kind}/{id}/observers", s.api("crm:write", s.apiCRMObservers))
	mux.HandleFunc("PUT /api/v1/crm/{kind}/{id}/utm", s.api("crm:write", s.apiCRMUTM))
	mux.HandleFunc("GET /api/v1/marketing/analytics", s.api("crm:read", s.apiMktAnalytics))
	mux.HandleFunc("GET /api/v1/marketing/sources", s.api("crm:read", s.apiMktSources))
	mux.HandleFunc("GET /api/v1/marketing/expenses", s.api("crm:read", s.apiMktExpenses))
	mux.HandleFunc("POST /api/v1/marketing/expenses", s.api("crm:write", s.apiMktAddExpense))
	mux.HandleFunc("DELETE /api/v1/marketing/expenses/{id}", s.api("crm:write", s.apiMktDeleteExpense))
	mux.HandleFunc("POST /api/v1/crm/leads/{id}/convert", s.api("crm:write", s.apiCRMConvert))
	mux.HandleFunc("POST /api/v1/crm/{kind}/{id}/comments", s.api("crm:write", s.apiCRMComment))
	mux.HandleFunc("GET /api/v1/crm/{kind}/{id}/timeline", s.api("crm:read", s.apiCRMTimeline))
	mux.HandleFunc("PATCH /api/v1/crm/{kind}/{id}/timeline/{ev}", s.api("crm:write", s.apiCRMEvent))
	mux.HandleFunc("DELETE /api/v1/crm/{kind}/{id}/timeline/{ev}", s.api("crm:write", s.apiCRMEvent))
	mux.HandleFunc("GET /api/v1/crm/{kind}/{id}/products", s.api("crm:read", s.apiCRMProducts))
	mux.HandleFunc("PUT /api/v1/crm/{kind}/{id}/products", s.api("crm:write", s.apiCRMSetProducts))
	mux.HandleFunc("GET /api/v1/crm/{kind}/{id}/requisites", s.api("crm:read", s.apiCRMRequisites))
	mux.HandleFunc("POST /api/v1/crm/{kind}/{id}/requisites", s.api("crm:write", s.apiCRMSaveRequisite))
	mux.HandleFunc("DELETE /api/v1/crm/{kind}/{id}/requisites/{req}", s.api("crm:write", s.apiCRMDeleteRequisite))
	mux.HandleFunc("GET /api/v1/bi/datasets", s.api("bi:read", s.apiBIDatasets))
	mux.HandleFunc("POST /api/v1/bi/query", s.api("bi:read", s.apiBIQuery))
	mux.HandleFunc("GET /api/v1/bi/export", s.apiBIExportSets) // a key of external BI or a personal one, see biExportAuth
	mux.HandleFunc("GET /api/v1/bi/export/{dataset}", s.apiBIExport)
	s.apiCollabRoutes(mux)
	s.apiCalendarRoutes(mux)
	s.apiDiskRoutes(mux)
	s.apiDocsRoutes(mux)
	s.apiFeedRoutes(mux)
	s.apiOpenLinesRoutes(mux)
	s.apiKBRoutes(mux)
	s.apiBPRoutes(mux)
	s.apiAIRoutes(mux)
	s.apiBookingRoutes(mux)
	s.apiAppsRoutes(mux)
}

type apiHandler func(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error

// api authenticates the key, checks the scope and renders errors as JSON.
func (s *Server) api(scope string, h apiHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			s.apiFail(w, r, errNoAuth)
			return
		}
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		token = strings.TrimSpace(token)
		var p apikeys.Principal
		var err error
		if apps.IsToken(token) && s.Apps != nil { // an application acting for an employee (OAuth 2.0)
			p, err = s.Apps.Authenticate(r.Context(), token, ip)
		} else {
			p, err = s.Keys.Authenticate(r.Context(), token, ip)
		}
		if errors.Is(err, apikeys.ErrInvalid) {
			s.audit(r, audit.Event{Kind: "api_bad_key", Target: r.Method + " " + r.URL.Path})
		}
		if err != nil {
			s.apiFail(w, r, err)
			return
		}
		if !s.ipAllowed(r, p.UserID) { // «Ограничение доступа по IP» holds for the employee's keys too
			s.apiFail(w, r, i18n.E("ipaccess.denied", audit.ClientIP(r)))
			return
		}
		if scope != "" && !p.Can(scope) {
			s.apiFail(w, r, i18n.E("err.api_no_scope", scope))
			return
		}
		guest, err := s.Tasks.IsGuest(r.Context(), p.UserID)
		if err != nil {
			s.apiFail(w, r, err)
			return
		}
		r = r.WithContext(tasks.WithViewer(r.Context(), p.UserID, guest))
		if !guestPass(r, guestAPI) { // a guest's key reaches only the sections of their collabs
			s.apiFail(w, r, tasks.ErrNotFound)
			return
		}
		// X-Go24-Admin: 1 — admin mode, as the switch on the pages: an administrator's key sees and runs everything
		// (all projects and collabs); every such request is logged
		if r.Header.Get("X-Go24-Admin") == "1" {
			if !s.apiAdmin(r.Context(), p.UserID) {
				s.apiFail(w, r, tasks.ErrForbidden)
				return
			}
			s.Log.Info("api admin mode", "user_id", p.UserID, "key_id", p.KeyID, "method", r.Method, "path", r.URL.Path)
			r = r.WithContext(tasks.WithAdmin(r.Context()))
		}
		if s.frozen(r) { // a collab in the archive is read-only
			s.apiFail(w, r, errCollabArchived)
			return
		}
		if err := h(w, r, p); err != nil {
			s.apiFail(w, r, err)
		}
	}
}

// apiAdmin: the user is an administrator of go24 (an employee in an administrators' group of Authentik, as their
// last sign-in told, or made one in «Настройки → Администраторы»).
func (s *Server) apiAdmin(ctx context.Context, userID string) bool {
	var yes bool
	_ = s.DB.QueryRow(ctx, `SELECT active AND NOT guest AND (admin OR groups && $2::text[]) FROM users WHERE id = $1`, userID, s.AdminGroups).Scan(&yes)
	return yes
}

// apiError: "error" is human-readable in the caller's Accept-Language, "code" is the stable locale key.
type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

var errNoAuth = i18n.E("err.api_no_auth")

// apiTr translates into the API caller's Accept-Language.
func (s *Server) apiTr(r *http.Request) func(string) string {
	lang := s.I18n.Match(r.Header.Get("Accept-Language"))
	return func(key string) string { return s.I18n.T(lang, key) }
}

func (s *Server) apiFail(w http.ResponseWriter, r *http.Request, err error) {
	lang := s.I18n.Match(r.Header.Get("Accept-Language"))
	var ie *i18n.Error
	if !errors.As(err, &ie) {
		s.Log.Error("api", "err", err)
		ie = i18n.E("err.internal")
	}
	status := http.StatusUnprocessableEntity
	switch {
	case errors.Is(err, apikeys.ErrInvalid), errors.Is(err, errNoAuth):
		status = http.StatusUnauthorized
	case errors.Is(err, tasks.ErrForbidden), ie.Key == "err.api_no_scope", ie.Key == "err.forbidden", ie.Key == "ipaccess.denied", ie.Key == "err.api_app_only":
		status = http.StatusForbidden
	case errors.Is(err, tasks.ErrNotFound), ie.Key == "err.not_found":
		status = http.StatusNotFound
	case errors.Is(err, tasks.ErrConflict), ie.Key == "booking.err.overlap":
		status = http.StatusConflict
	case ie.Key == "err.internal":
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, apiError{Error: s.I18n.Err(lang, ie), Code: ie.Key})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return i18n.E("err.bad_json", err.Error())
	}
	return nil
}

// ---------- DTO ----------

type apiUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type apiTask struct {
	Key             string     `json:"key"`
	ID              string     `json:"id"`
	Project         string     `json:"project"`
	Number          int        `json:"number"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	Status          string     `json:"status"`
	StatusTitle     string     `json:"status_title"`
	Priority        int        `json:"priority"`
	Creator         apiUser    `json:"creator"`
	Responsible     apiUser    `json:"responsible"`
	Parent          string     `json:"parent,omitempty"`
	RequiresControl bool       `json:"requires_control"`
	Deadline        *time.Time `json:"deadline"`
	Overdue         bool       `json:"overdue"`
	Version         int        `json:"version"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	ClosedAt        *time.Time `json:"closed_at"`
	EstimateMinutes *int       `json:"estimate_minutes"`
	StartPlan       string     `json:"start_plan,omitempty"` // YYYY-MM-DD
	EndPlan         string     `json:"end_plan,omitempty"`
	// the task parameters (SE_PARAMETER 1–3)
	DatesFromSubtasks bool `json:"dates_from_subtasks"`
	AutoComplete      bool `json:"auto_complete"`
	ResultRequired    bool `json:"result_required"`
}

func planDate(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}

func toAPITask(t tasks.Task, tr func(string) string) apiTask {
	return apiTask{
		Key: t.Key(), ID: t.ID, Project: t.ProjectKey, Number: t.Number, Title: t.Title, Description: t.Description,
		Status: string(t.Status), StatusTitle: tr("status." + string(t.Status)), Priority: int(t.Priority),
		Creator: apiUser{t.CreatorID, t.CreatorName}, Responsible: apiUser{t.ResponsibleID, t.ResponsibleName},
		Parent: t.ParentKey, RequiresControl: t.RequiresControl, Deadline: t.Deadline, Overdue: t.Overdue(),
		Version: t.Version, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, ClosedAt: t.ClosedAt,
		EstimateMinutes: t.EstimateMinutes, StartPlan: planDate(t.StartPlan), EndPlan: planDate(t.EndPlan),
		DatesFromSubtasks: t.DatesFromSubtasks, AutoComplete: t.AutoComplete, ResultRequired: t.ResultRequired,
	}
}

func toAPITasks(list []tasks.Task, tr func(string) string) []apiTask {
	out := make([]apiTask, 0, len(list))
	for _, t := range list {
		out = append(out, toAPITask(t, tr))
	}
	return out
}

func toAPIUsers(list []tasks.User) []apiUser {
	out := make([]apiUser, 0, len(list))
	for _, u := range list {
		out = append(out, apiUser{u.ID, u.Name})
	}
	return out
}

type apiChecklistItem struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Done      bool   `json:"done"`
	Important bool   `json:"important"`
	List      string `json:"list"` // the checklist the item is in (several in a task
}

type apiComment struct {
	ID        string    `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

type apiCard struct {
	apiTask
	Accomplices []apiUser          `json:"accomplices"`
	Auditors    []apiUser          `json:"auditors"`
	Checklist   []apiChecklistItem `json:"checklist"`
	Comments    []apiComment       `json:"comments"`
	Subtasks    []apiTask          `json:"subtasks"`
	Actions     []string           `json:"actions"` // what the key's user may do now
	TimeSpent   int                `json:"time_spent_seconds"`
	History     []apiHistory       `json:"history"` // changes, oldest first (the portal tasks.task.history.list)
	Tags        []string           `json:"tags"`
	Links       []apiLink          `json:"links"` // «Связанные задачи»: the task follows pred, precedes succ
	Files       []apiFile          `json:"files"`
	Views       int                `json:"views"` // «Просмотры»: how many people opened the card (GET …/views — who)
}

type apiLink struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"` // pred (this task waits for it) | succ (it waits for this task)
	Key   string `json:"key"`
	Title string `json:"title"`
}

type apiFile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	URL         string `json:"url"`
}

type apiHistory struct {
	Field     string    `json:"field"`
	Old       string    `json:"old"`
	New       string    `json:"new"`
	Actor     string    `json:"actor"`
	CreatedAt time.Time `json:"created_at"`
}

type apiProject struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MyRole      string `json:"my_role"`
	OpenTasks   int    `json:"open_tasks"`
}

func toAPIProject(p tasks.Project) apiProject {
	return apiProject{p.Key, p.Name, p.Description, p.MyRole, p.OpenTasks}
}

// ---------- handlers ----------

func (s *Server) apiMe(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var name, email string
	if err := s.DB.QueryRow(r.Context(), `SELECT name, email FROM users WHERE id = $1`, p.UserID).Scan(&name, &email); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": p.UserID, "name": name, "email": email, "key_id": p.KeyID, "scopes": p.Scopes})
	return nil
}

func (s *Server) apiProjects(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Tasks.ListProjects(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	out := make([]apiProject, 0, len(list))
	for _, pr := range list {
		out = append(out, toAPIProject(pr))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiCreateProject(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Key         string `json:"key"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	key, err := s.Tasks.CreateProject(r.Context(), p.UserID, in.Key, in.Name, in.Description)
	if err != nil {
		return err
	}
	s.Matrix.Wake() // the project's messenger room
	pr, err := s.Tasks.ProjectByKey(r.Context(), p.UserID, key)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, toAPIProject(pr))
	return nil
}

func (s *Server) apiProject(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	pr, err := s.Tasks.ProjectByKey(r.Context(), p.UserID, r.PathValue("key"))
	if err != nil {
		return err
	}
	members, err := s.Tasks.Members(r.Context(), pr.ID)
	if err != nil {
		return err
	}
	type apiMember struct {
		apiUser
		Role string `json:"role"`
	}
	ms := make([]apiMember, 0, len(members))
	for _, m := range members {
		ms = append(ms, apiMember{apiUser{m.ID, m.Name}, m.Role})
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": toAPIProject(pr), "members": ms})
	return nil
}

func (s *Server) apiProjectTasks(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	pr, err := s.Tasks.ProjectByKey(r.Context(), p.UserID, r.PathValue("key"))
	if err != nil {
		return err
	}
	list, err := s.Tasks.ListTasks(r.Context(), tasks.Filter{ProjectID: pr.ID, OnlyOpen: r.URL.Query().Get("all") != "1", Viewer: p.UserID})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toAPITasks(list, s.apiTr(r)))
	return nil
}

func (s *Server) apiMyTasks(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	role := r.URL.Query().Get("role")
	if role == "" {
		role = "responsible"
	}
	list, err := s.Tasks.ListTasks(r.Context(), tasks.Filter{UserID: p.UserID, Role: role, OnlyOpen: r.URL.Query().Get("all") != "1"})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toAPITasks(list, s.apiTr(r)))
	return nil
}

type apiTaskInput struct {
	Title             *string    `json:"title"`
	Description       *string    `json:"description"`
	ResponsibleID     *string    `json:"responsible_id"`
	Priority          *int       `json:"priority"`
	Deadline          *time.Time `json:"deadline"`
	ClearDeadline     bool       `json:"clear_deadline"`
	RequiresControl   *bool      `json:"requires_control"`
	EstimateMinutes   *int       `json:"estimate_minutes"`
	ClearEstimate     bool       `json:"clear_estimate"`
	StartPlan         *string    `json:"start_plan"` // "YYYY-MM-DD", "" clears
	EndPlan           *string    `json:"end_plan"`
	Parent            string     `json:"parent"`
	Accomplices       []string   `json:"accomplices"`
	Auditors          []string   `json:"auditors"`
	Version           int        `json:"version"`
	DatesFromSubtasks *bool      `json:"dates_from_subtasks"`
	AutoComplete      *bool      `json:"auto_complete"`
	ResultRequired    *bool      `json:"result_required"`
}

// params are the task parameters of the input, the missing ones as in cur.
func (in apiTaskInput) params(cur tasks.Params) tasks.Params {
	return tasks.Params{DatesFromSubtasks: deref(in.DatesFromSubtasks, cur.DatesFromSubtasks),
		AutoComplete: deref(in.AutoComplete, cur.AutoComplete), ResultRequired: deref(in.ResultRequired, cur.ResultRequired)}
}

func deref[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

func (s *Server) apiCreateTask(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	pr, err := s.Tasks.ProjectByKey(r.Context(), p.UserID, r.PathValue("key"))
	if err != nil {
		return err
	}
	var in apiTaskInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	key, err := s.Tasks.CreateTask(r.Context(), p.UserID, pr, tasks.NewTask{
		Title:           deref(in.Title, ""),
		Description:     deref(in.Description, ""),
		ResponsibleID:   deref(in.ResponsibleID, ""),
		Priority:        tasks.Priority(deref(in.Priority, 1)),
		Deadline:        in.Deadline,
		RequiresControl: deref(in.RequiresControl, false),
		ParentKey:       in.Parent,
		Accomplices:     in.Accomplices,
		Auditors:        in.Auditors,
		Params:          in.params(tasks.Params{}),
	})
	if err != nil {
		return err
	}
	return s.writeCard(w, r, p, key, http.StatusCreated)
}

func (s *Server) apiTask(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	return s.writeCard(w, r, p, r.PathValue("key"), http.StatusOK)
}

// apiUpdateTask applies a partial update; "version" from the last read is required (optimistic locking).
func (s *Server) apiUpdateTask(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in apiTaskInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Version == 0 {
		return i18n.E("err.version_required")
	}
	id, err := s.Tasks.TaskIDByKey(r.Context(), r.PathValue("key"))
	if err != nil {
		return err
	}
	cur, err := s.Tasks.GetCard(r.Context(), p.UserID, id)
	if err != nil {
		return err
	}
	deadline := cur.Deadline
	if in.Deadline != nil {
		deadline = in.Deadline
	}
	if in.ClearDeadline {
		deadline = nil
	}
	startPlan, endPlan := cur.StartPlan, cur.EndPlan
	if in.StartPlan != nil {
		if startPlan, err = parseDate(*in.StartPlan); err != nil {
			return err
		}
	}
	if in.EndPlan != nil {
		if endPlan, err = parseDate(*in.EndPlan); err != nil {
			return err
		}
	}
	estimate := cur.EstimateMinutes
	if in.EstimateMinutes != nil {
		estimate = in.EstimateMinutes
	}
	if in.ClearEstimate {
		estimate = nil
	}
	if err := s.Tasks.UpdateTask(r.Context(), p.UserID, id, tasks.TaskUpdate{
		Version:         in.Version,
		Title:           deref(in.Title, cur.Title),
		Description:     deref(in.Description, cur.Description),
		ResponsibleID:   deref(in.ResponsibleID, cur.ResponsibleID),
		Priority:        tasks.Priority(deref(in.Priority, int(cur.Priority))),
		Deadline:        deadline,
		RequiresControl: deref(in.RequiresControl, cur.RequiresControl),
		EstimateMinutes: estimate,
		StartPlan:       startPlan,
		EndPlan:         endPlan,
		// the deadline rules are not in the input: they stay as they are
		AllowChangeDeadline: cur.AllowChangeDeadline,
		MatchWorkTime:       cur.MatchWorkTime,
		Params:              in.params(cur.Params),
	}); err != nil {
		return err
	}
	return s.writeCard(w, r, p, r.PathValue("key"), http.StatusOK)
}

func (s *Server) apiAction(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	return s.apiTaskOp(w, r, p, func(id string) error {
		return s.Tasks.ApplyAction(r.Context(), p.UserID, id, tasks.Action(r.PathValue("action")))
	})
}

func (s *Server) apiComment(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Body string `json:"body"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	return s.apiTaskOp(w, r, p, func(id string) error {
		if s.Matrix != nil { // as in the web: the message is kept in the messenger first
			_, err := s.Matrix.PostTaskComment(r.Context(), p.UserID, id, in.Body)
			return err
		}
		return s.Tasks.AddComment(r.Context(), p.UserID, id, in.Body)
	})
}

func (s *Server) apiChecklistAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Title string `json:"title"`
		List  string `json:"list"` // "" — the first checklist
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	return s.apiTaskOp(w, r, p, func(id string) error {
		return s.Tasks.AddChecklistItems(r.Context(), p.UserID, id, in.List, in.Title)
	})
}

func (s *Server) apiChecklistToggle(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	return s.apiTaskOp(w, r, p, func(id string) error {
		return s.Tasks.ChangeChecklistItem(r.Context(), p.UserID, id, r.PathValue("id"), false)
	})
}

// apiDeleteTask moves the task to the trash (the portal tasks.task.delete): {"result": true}.
func (s *Server) apiDeleteTask(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id, err := s.Tasks.TaskIDByKey(r.Context(), r.PathValue("key"))
	if err != nil {
		return err
	}
	if err := s.Tasks.DeleteTask(r.Context(), p.UserID, id); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": true})
	return nil
}

// apiEfficiency: the key owner's efficiency for ?from=&to= (dates, the last day included; the current month by default).
func (s *Server) apiEfficiency(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	now := time.Now()
	from, to := monthOf(now)
	if v, err := parseDate(r.FormValue("from")); err != nil {
		return err
	} else if v != nil {
		from = time.Date(v.Year(), v.Month(), v.Day(), 0, 0, 0, 0, time.Local)
	}
	if v, err := parseDate(r.FormValue("to")); err != nil {
		return err
	} else if v != nil {
		to = time.Date(v.Year(), v.Month(), v.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, 1)
	}
	e, err := s.Tasks.EfficiencyOf(r.Context(), p.UserID, from, to, now)
	if err != nil {
		return err
	}
	vs := make([]map[string]any, 0, len(e.Violations))
	for _, v := range e.Violations {
		vs = append(vs, map[string]any{"key": v.Key, "title": v.Title, "kind": v.Kind, "at": v.At})
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "percent": e.Percent, "total": e.Total,
		"violated": e.Violated, "violations": vs})
	return nil
}

func toAPIFlow(f tasks.Flow) map[string]any {
	members := make([]string, len(f.Members))
	for i, u := range f.Members {
		members[i] = u.ID
	}
	return map[string]any{"id": f.ID, "name": f.Name, "description": f.Description, "project": f.ProjectKey, "owner_id": f.OwnerID,
		"moderator_id": f.ModeratorID, "distribution": f.Distribution, "planned_minutes": f.PlannedMinutes, "active": f.Active,
		"members": members, "can_create": f.CanCreate,
		"stats": map[string]any{"queue": f.Stats.Queue, "in_work": f.Stats.InWork, "done": f.Stats.Done,
			"avg_queue_minutes": f.Stats.AvgQueue, "avg_work_minutes": f.Stats.AvgWork, "efficiency": f.Stats.Efficiency}}
}

// apiFlows lists the flows the key's owner sees (the portal tasks.flow.Flow.get for each).
func (s *Server) apiFlows(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Tasks.Flows(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	out := make([]map[string]any, len(list))
	for i, f := range list {
		out[i] = toAPIFlow(f)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiFlowCreate makes a flow (tasks.flow.Flow.create): {name, project, distribution, planned_minutes, members, …}.
func (s *Server) apiFlowCreate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Name           string   `json:"name"`
		Description    string   `json:"description"`
		Project        string   `json:"project"`
		OwnerID        string   `json:"owner_id"`
		ModeratorID    string   `json:"moderator_id"`
		Distribution   string   `json:"distribution"`
		PlannedMinutes int      `json:"planned_minutes"`
		Members        []string `json:"members"`
		Creators       []string `json:"creators"` // empty: everyone may put tasks in
		MatchWorkTime  *bool    `json:"match_work_time"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	id, err := s.Tasks.SaveFlow(r.Context(), p.UserID, tasks.FlowInput{Name: in.Name, Description: in.Description,
		ProjectKey: in.Project, OwnerID: in.OwnerID, ModeratorID: in.ModeratorID, Distribution: in.Distribution,
		PlannedMinutes: in.PlannedMinutes, Members: in.Members, Creators: in.Creators, CreatorsAll: len(in.Creators) == 0,
		MatchWorkTime: in.MatchWorkTime == nil || *in.MatchWorkTime, Active: true})
	if err != nil {
		return err
	}
	f, err := s.Tasks.Flow(r.Context(), p.UserID, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, toAPIFlow(f))
	return nil
}

// apiFlowTask puts a task into the flow (tasks.task.add with FLOW_ID): {title, description}; the flow sets the rest.
func (s *Server) apiFlowTask(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	key, err := s.Tasks.CreateFlowTask(r.Context(), p.UserID, r.PathValue("id"), tasks.FlowTask{Title: in.Title, Description: in.Description})
	if err != nil {
		return err
	}
	return s.writeCard(w, r, p, key, http.StatusCreated)
}

// apiTake: «Взять в работу» a task waiting in a self-assign flow.
func (s *Server) apiTake(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	return s.apiTaskOp(w, r, p, func(id string) error { return s.Tasks.TakeFlowTask(r.Context(), p.UserID, id) })
}

// apiTrash lists the deleted tasks the key's owner sees.
func (s *Server) apiTrash(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Tasks.Trash(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	out := make([]map[string]any, 0, len(list))
	for _, t := range list {
		out = append(out, map[string]any{"id": t.ID, "key": t.Key, "title": t.Title, "project": t.ProjectKey,
			"deleted_at": t.DeletedAt, "deleted_by": t.DeletedByName, "subtasks": t.Subtasks, "purge_at": t.PurgeAt})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiTrashRestore brings a deleted task back and answers with its card.
func (s *Server) apiTrashRestore(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.Tasks.RestoreTask(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		return err
	}
	t, err := s.Tasks.TaskByID(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return s.writeCard(w, r, p, t.Key(), http.StatusOK)
}

func (s *Server) apiChecklistDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	return s.apiTaskOp(w, r, p, func(id string) error {
		return s.Tasks.ChangeChecklistItem(r.Context(), p.UserID, id, r.PathValue("id"), true)
	})
}

func (s *Server) apiParticipantAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		UserID string `json:"user_id"`
		Role   string `json:"role"` // accomplice | auditor
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	return s.apiTaskOp(w, r, p, func(id string) error {
		return s.Tasks.SetParticipant(r.Context(), p.UserID, id, in.UserID, in.Role, true)
	})
}

func (s *Server) apiParticipantRemove(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	return s.apiTaskOp(w, r, p, func(id string) error {
		return s.Tasks.SetParticipant(r.Context(), p.UserID, id, r.PathValue("uid"), r.PathValue("role"), false)
	})
}

// apiTaskOp resolves {key}, runs op and responds with the fresh task card.
func (s *Server) apiTaskOp(w http.ResponseWriter, r *http.Request, p apikeys.Principal, op func(id string) error) error {
	id, err := s.Tasks.TaskIDByKey(r.Context(), r.PathValue("key"))
	if err != nil {
		return err
	}
	if err := op(id); err != nil {
		return err
	}
	return s.writeCard(w, r, p, r.PathValue("key"), http.StatusOK)
}

func (s *Server) writeCard(w http.ResponseWriter, r *http.Request, p apikeys.Principal, key string, status int) error {
	id, err := s.Tasks.TaskIDByKey(r.Context(), key)
	if err != nil {
		return err
	}
	c, err := s.Tasks.GetCard(r.Context(), p.UserID, id)
	if err != nil {
		return err
	}
	out := apiCard{
		apiTask:     toAPITask(c.Task, s.apiTr(r)),
		Accomplices: toAPIUsers(c.Accomplices),
		Auditors:    toAPIUsers(c.Auditors),
		Checklist:   make([]apiChecklistItem, 0, len(c.Checklist)),
		Comments:    make([]apiComment, 0, len(c.Comments)),
		Subtasks:    toAPITasks(c.Subtasks, s.apiTr(r)),
		Actions:     make([]string, 0, len(c.Actions)),
		TimeSpent:   c.TimeTotal,
	}
	for _, i := range c.Checklist {
		out.Checklist = append(out.Checklist, apiChecklistItem{i.ID, i.Title, i.Done, i.Important, i.ListID})
	}
	for _, m := range c.Comments {
		out.Comments = append(out.Comments, apiComment{m.ID, m.AuthorName, m.Body, m.CreatedAt})
	}
	out.History = make([]apiHistory, 0, len(c.History))
	for _, h := range c.History {
		out.History = append(out.History, apiHistory{h.Field, h.Old, h.New, h.ActorName, h.CreatedAt})
	}
	for _, a := range c.Actions {
		out.Actions = append(out.Actions, string(a))
	}
	one := []tasks.Task{c.Task}
	if err := s.Tasks.FillMarks(r.Context(), "", one); err != nil {
		return err
	}
	out.Tags = append([]string{}, one[0].Tags...)
	pred, succ, err := s.Tasks.TaskLinks(r.Context(), id)
	if err != nil {
		return err
	}
	out.Links = []apiLink{}
	for _, l := range pred {
		out.Links = append(out.Links, apiLink{l.ID, "pred", l.FromKey, l.FromTitle})
	}
	for _, l := range succ {
		out.Links = append(out.Links, apiLink{l.ID, "succ", l.ToKey, l.ToTitle})
	}
	files, err := s.Storages.TaskFiles(r.Context(), id)
	if err != nil {
		return err
	}
	out.Files = []apiFile{}
	for _, f := range files {
		out.Files = append(out.Files, apiFile{f.ID, f.Name, f.ContentType, f.Size, "/files/" + f.ID})
	}
	viewers, err := s.Tasks.Viewers(r.Context(), p.UserID, id)
	if err != nil {
		return err
	}
	out.Views = len(viewers)
	writeJSON(w, status, out)
	return nil
}

// apiTaskViews: who opened the task's card — the first and the last time, how many times; the latest first.
func (s *Server) apiTaskViews(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id, err := s.Tasks.TaskIDByKey(r.Context(), r.PathValue("key"))
	if err != nil {
		return err
	}
	list, err := s.Tasks.Viewers(r.Context(), p.UserID, id)
	if err != nil {
		return err
	}
	type view struct {
		User  apiUser   `json:"user"`
		First time.Time `json:"first_at"`
		Last  time.Time `json:"viewed_at"`
		Times int       `json:"times"`
	}
	out := make([]view, len(list))
	for i, v := range list {
		out[i] = view{apiUser{ID: v.ID, Name: v.Name}, v.First, v.Last, v.Times}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiTag adds a tag to the task or removes it: {"tag": "почта", "remove": false}.
func (s *Server) apiTag(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Tag    string `json:"tag"`
		Remove bool   `json:"remove"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	return s.apiTaskOp(w, r, p, func(id string) error { return s.Tasks.SetTag(r.Context(), p.UserID, id, in.Tag, !in.Remove) })
}

// apiLinkAdd links a task to this one («Связанные задачи»): {"pred": "GO-1"} — this task follows GO-1.
func (s *Server) apiLinkAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Pred string `json:"pred"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	return s.apiTaskOp(w, r, p, func(id string) error { return s.Tasks.AddLink(r.Context(), p.UserID, id, in.Pred) })
}

func (s *Server) apiLinkDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	return s.apiTaskOp(w, r, p, func(id string) error { return s.Tasks.RemoveLink(r.Context(), p.UserID, id, r.PathValue("id")) })
}

// apiFileAdd attaches a file to the task (multipart, field «file»; a screenshot, a log) into the built-in storage.
func (s *Server) apiFileAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	key := r.PathValue("key")
	id, err := s.Tasks.TaskIDByKey(r.Context(), key)
	if err != nil {
		return err
	}
	if err := s.Tasks.CheckVisible(r.Context(), p.UserID, id); err != nil {
		return err
	}
	if err := s.Tasks.CanAttach(r.Context(), p.UserID, id); err != nil {
		return err
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		return i18n.E("err.file_required")
	}
	defer f.Close()
	ct := hdr.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	name := path.Base(strings.ReplaceAll(hdr.Filename, "\\", "/"))
	obj := path.Join("go24", time.Now().Format("2006/01"), randomHex(16), name)
	if err := s.Storages.Put(r.Context(), p.UserID, false, "", obj, f, hdr.Size, ct); err != nil {
		return err
	}
	fileID, err := s.Storages.AddFile(r.Context(), storages.NewFile{Key: obj, Name: name, ContentType: ct, Size: hdr.Size,
		TaskID: id, UploadedBy: p.UserID})
	if err != nil {
		return err
	}
	s.keepUploaded(r, auth.Session{UserID: p.UserID}, fileID)
	if err := s.Tasks.FileAdded(r.Context(), p.UserID, id, name); err != nil {
		s.Log.Error("file history", "err", err)
	}
	return s.writeCard(w, r, p, key, http.StatusCreated)
}

// apiTimeAdd logs time: {"duration": "1:30" | "90m" | "1.5", "date": "2026-10-11", "comment": "…"}.
func (s *Server) apiTimeAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Duration string `json:"duration"`
		Date     string `json:"date"`
		Comment  string `json:"comment"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	secs, err := tasks.ParseDuration(in.Duration)
	if err != nil {
		return err
	}
	day := time.Now()
	if in.Date != "" {
		if day, err = time.ParseInLocation("2006-01-02", in.Date, time.Local); err != nil {
			return i18n.E("err.bad_date")
		}
	}
	return s.apiTaskOp(w, r, p, func(id string) error { return s.Tasks.AddTime(r.Context(), p.UserID, id, secs, day, in.Comment) })
}

func (s *Server) apiTimerStart(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	return s.apiTaskOp(w, r, p, func(id string) error { return s.Tasks.StartTimer(r.Context(), p.UserID, id) })
}

func (s *Server) apiTimerStop(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.Tasks.StopTimer(r.Context(), p.UserID); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"stopped": true})
	return nil
}

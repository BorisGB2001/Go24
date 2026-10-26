// Package web serves the go24 UI: login via Authentik, the task tracker and files.
package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/ai"
	"go24/internal/apikeys"
	"go24/internal/apps"
	"go24/internal/audit"
	"go24/internal/backup"
	"go24/internal/auth"
	"go24/internal/authentik"
	"go24/internal/bi"
	"go24/internal/booking"
	"go24/internal/bp"
	"go24/internal/calendar"
	"go24/internal/calls"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/drive"
	"go24/internal/feed"
	"go24/internal/hooks"
	"go24/internal/i18n"
	"go24/internal/ipaccess"
	"go24/internal/kb"
	"go24/internal/linkpreview"
	"go24/internal/live"
	"go24/internal/mail"
	"go24/internal/marketing"
	"go24/internal/matrixsync"
	"go24/internal/menu"
	"go24/internal/notify"
	"go24/internal/notifyout"
	"go24/internal/office"
	"go24/internal/openlines"
	"go24/internal/portal"
	"go24/internal/sites"
	"go24/internal/speech"
	"go24/internal/sign"
	"go24/internal/storages"
	"go24/internal/tasks"
	"go24/internal/telephony"
)

const (
	stateCookie   = "go24_oidc_state"
	backCookie    = "go24_back" // the page asked for before signing in, opened after it
	maxUploadSize = 100 << 20 // 100 MB
)

type Server struct {
	DB       *pgxpool.Pool
	Storages *storages.Store
	Tasks    *tasks.Store
	Company  *company.Store
	// Authentik fills missing user data from employee cards (nil when GO24_AUTHENTIK_URL/TOKEN are not set)
	Authentik       *authentik.Client
	AuthentikPublic string // the Authentik address people open (sign-up links), from the OIDC issuer
	// Matrix keeps the messenger in step (nil when the Matrix server is not configured)
	Matrix *matrixsync.Service
	// Live pushes new task chat messages to open cards (nil: cards update on reload only)
	Live        *live.Broker
	Feed        *feed.Store
	KB          *kb.Store
	Booking     *booking.Store
	Sign        *sign.Store
	Hooks       *hooks.Store
	Apps        *apps.Store
	Audit       *audit.Store    // «Журнал событий»; nil — not kept
	IPAccess    *ipaccess.Store // «Ограничение доступа по IP»; nil — none
	Backup      *backup.Store   // «Резервное копирование»; nil — no backups' folder
	// BackupHostDir is the backups' folder as the server names it (GO24_BACKUP_HOST_DIR); BackupRC — rclone-rcd for
	// the outside places of the copies (nil — none)
	BackupHostDir string
	BackupRC      backup.Remote
	OpenLines   *openlines.Store
	Sites       *sites.Store
	BP          *bp.Store
	Mail        *mail.Store
	AI          *ai.Store // connections of AI providers (nil: «ИИ» is off)
	CRM         *crm.Store
	BI          *bi.Store
	Marketing   *marketing.Store
	Drive       *drive.Store
	Calls       *calls.Provider      // nil: calls are off
	Rings       *calls.Rings         // a call from a chat rings its people (nil: it does not)
	CallLines   *calls.Sessions      // a call of a chat has its line in the chat (nil: it has not)
	CallGuests  *calls.Guests        // guest links of calls (nil: none)
	Recordings  *calls.Recordings    // recordings of calls (nil: calls are not recorded)
	RecordDir   string               // where LiveKit Egress puts the recordings, as go24 sees that folder
	External    *calls.External      // external services of calls: Zoom, Телемост, VK, Telegram, Max (nil: none)
	NotifyOut   *notifyout.Service   // notifications in outside messengers: Telegram, MAX… (nil: none)
	Telephony   *telephony.PBX       // own Asterisk (nil: no telephony)
	Speech      *speech.Client       // Whisper for the text of voice messages; nil — none
	Office      *office.Server       // «Документы онлайн» (ONLYOFFICE); nil — files are only downloaded
	Links       *linkpreview.Fetcher // cards of links in chat messages; nil — none
	CallURL     string               // go24 over https for calls ("" — BaseURL)
	Notify      *notify.Store
	Calendar    *calendar.Store
	Keys        *apikeys.Store
	Portal      *portal.Store // general settings of the portal; nil — the defaults
	Menus       *menu.Store   // the left menus employees set up; nil — go24's menu for all
	BaseURL     string
	ThemeDir    string // optional GO24_THEME_DIR with theme.css overriding the default look
	I18n        *i18n.Bundle
	AdminGroups []string // Authentik groups that make a user a go24 administrator
	GuestGroups []string // Authentik groups of guests of collabs (they see their collabs only)

	pages    map[string]map[string]*template.Template // language → page → template
	OIDC     *auth.OIDC                               // nil when Authentik is not configured yet
	Sessions *auth.Sessions
	Log      *slog.Logger
}

func (s *Server) Routes() http.Handler {
	if s.I18n == nil {
		b, err := i18n.Load("")
		if err != nil {
			panic(err)
		}
		s.I18n = b
	}
	pages, err := buildPages(s.I18n)
	if err != nil {
		panic(err)
	}
	s.pages = pages

	mux := http.NewServeMux()
	mux.HandleFunc("GET /lang/{code}", s.setLang)
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /login", s.login)
	mux.HandleFunc("GET /auth/callback", s.callback)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /{$}", s.home)
	mux.Handle("GET /static/", staticHandler())
	if s.ThemeDir != "" {
		mux.Handle("GET /theme/", themeHandler(s.ThemeDir))
	}
	mux.HandleFunc("GET /search", s.requireUser(s.search))

	mux.HandleFunc("GET /my", s.requireUser(s.myTasks))
	mux.HandleFunc("GET /feed", s.requireUser(s.feedPage))
	mux.HandleFunc("GET /call", s.requireUser(s.callPage))
	mux.HandleFunc("GET /call/user", s.requireUser(s.callUser))
	mux.HandleFunc("POST /call/ring/{id}/{action}", s.requireUser(s.callRing))
	mux.HandleFunc("POST /call/ring-more", s.requireUser(s.callRingMore))
	mux.HandleFunc("GET /call/guests", s.requireUser(s.callGuests))
	mux.HandleFunc("POST /call/guests", s.requireUser(s.callGuests))
	mux.HandleFunc("POST /call/guests/{id}/revoke", s.requireUser(s.callGuestRevoke))
	mux.HandleFunc("POST /call/lobby/{id}/{action}", s.requireUser(s.callLobby))
	mux.HandleFunc("GET /call/record", s.requireUser(s.callRecord))
	mux.HandleFunc("POST /call/record", s.requireUser(s.callRecord))
	mux.HandleFunc("GET /call/g/{token}", s.guestPage) // guest links of calls: no sign-in
	mux.HandleFunc("POST /call/g/{token}", s.guestArrive)
	mux.HandleFunc("GET /call/g/{token}/state", s.guestState)
	mux.HandleFunc("GET /crm", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/crm/deals", http.StatusFound) })
	mux.HandleFunc("GET /crm/documents", s.requireUser(s.docTemplatesPage))
	mux.HandleFunc("POST /crm/documents", s.requireUser(s.docTemplateAdd))
	mux.HandleFunc("GET /crm/documents/{id}/edit", s.requireUser(s.docTemplateEdit))
	mux.HandleFunc("POST /crm/documents/my-company", s.requireUser(s.docMyCompany))
	mux.HandleFunc("POST /crm/documents/builtin", s.requireUser(s.docTemplateBuiltin))
	mux.HandleFunc("POST /crm/documents/{id}/delete", s.requireUser(s.docTemplateDelete))
	mux.HandleFunc("POST /crm/documents/{id}/numerator", s.requireUser(s.docTemplateNumerator))
	mux.HandleFunc("POST /crm/documents/{id}/access", s.requireUser(s.docTemplateAccess))
	mux.HandleFunc("POST /crm/{kind}/{id}/document", s.requireUser(s.crmDocument))
	mux.HandleFunc("GET /crm/{kind}/{id}/document", s.requireUser(s.crmDocumentForm))
	mux.HandleFunc("GET /crm/{kind}/{id}/documents/{doc}", s.requireUser(s.crmDocumentFile))
	mux.HandleFunc("POST /crm/{kind}/{id}/documents/{doc}", s.requireUser(s.crmDocumentRemake))
	mux.HandleFunc("POST /crm/{kind}/{id}/documents/{doc}/delete", s.requireUser(s.crmDocumentDelete))
	mux.HandleFunc("GET /crm/{kind}/{id}/documents/{doc}/pdf", s.requireUser(s.crmDocumentPDF))
	mux.HandleFunc("POST /crm/{kind}/{id}/documents/{doc}/public", s.requireUser(s.crmDocumentPublic))
	// «Подпись»: documents sent for signing to the employee and the client (sign.go); the client's page — no sign-in
	mux.HandleFunc("GET /sign", s.requireUser(s.signPage))
	mux.HandleFunc("POST /sign", s.requireUser(s.signCreate))
	mux.HandleFunc("GET /sign/new", s.requireUser(s.signNew))
	mux.HandleFunc("GET /sign/{id}", s.requireUser(s.signDoc))
	mux.HandleFunc("GET /sign/{id}/file/{which}", s.requireUser(s.signFile))
	mux.HandleFunc("POST /sign/{id}/sign", s.requireUser(s.signSign))
	mux.HandleFunc("POST /sign/{id}/cancel", s.requireUser(s.signCancel))
	mux.HandleFunc("POST /sign/{id}/resend", s.requireUser(s.signResend))
	mux.HandleFunc("GET /bk/{token}", s.bookingPublic)
	mux.HandleFunc("POST /bk/{token}", s.bookingPublicBook)
	mux.HandleFunc("GET /bk/b/{token}", s.bookingClient)
	mux.HandleFunc("POST /bk/b/{token}/status", s.bookingClientStatus)
	mux.HandleFunc("GET /cs/{token}", s.slotPublic) // «Поделиться слотами» of the calendar: no sign-in
	mux.HandleFunc("POST /cs/{token}", s.slotPublicBook)
	mux.HandleFunc("GET /cs/m/{token}", s.slotMeeting)
	mux.HandleFunc("POST /cs/m/{token}/cancel", s.slotMeetingCancel)
	mux.HandleFunc("GET /cs/m/{token}/ics", s.slotMeetingICS)
	mux.HandleFunc("POST /cs/m/{token}/call", s.slotMeetingCall)
	mux.HandleFunc("GET /cf/{token}", s.crmFormPublic)
	mux.HandleFunc("POST /cf/{token}", s.crmFormSend)
	mux.HandleFunc("GET /sg/{token}", s.signPublic)
	mux.HandleFunc("GET /sg/{token}/file/{which}", s.signPublicFile)
	mux.HandleFunc("POST /sg/{token}/code", s.signCode)
	mux.HandleFunc("POST /sg/{token}/sign", s.signPublicSign)
	mux.HandleFunc("GET /d/{token}", s.publicDocument) // public links of the documents of CRM cards: no sign-in
	mux.HandleFunc("GET /d/{token}/{format}", s.publicDocumentFile)
	mux.HandleFunc("GET /q/{token}", s.publicInvoice) // the client's link of a quote or an invoice
	mux.HandleFunc("GET /crm/roles", s.requireUser(s.crmRoles))
	mux.HandleFunc("POST /crm/roles/{id}", s.requireUser(s.crmRoleSave))
	mux.HandleFunc("POST /crm/roles/{id}/delete", s.requireUser(s.crmRoleDelete))
	mux.HandleFunc("POST /crm/roles/{id}/members", s.requireUser(s.crmRoleMember))
	mux.HandleFunc("POST /crm/stages", s.requireUser(s.crmStages))
	mux.HandleFunc("POST /crm/presets", s.requireUser(s.crmPresetSave))
	mux.HandleFunc("POST /crm/columns", s.requireUser(s.crmSetColumns))
	mux.HandleFunc("POST /crm/presets/{id}", s.requireUser(s.crmPresetAction))
	mux.HandleFunc("GET /crm/dicts", s.requireUser(s.crmDicts))
	mux.HandleFunc("POST /crm/dicts", s.requireUser(s.crmDictSave))
	mux.HandleFunc("GET /crm/recycle", s.requireUser(s.crmRecycle))
	mux.HandleFunc("POST /crm/recycle", s.requireUser(s.crmRecycleAction))
	mux.HandleFunc("POST /crm/recycle/{id}", s.requireUser(s.crmRecycleAction))
	mux.HandleFunc("GET /crm/forms", s.requireUser(s.crmForms))
	mux.HandleFunc("GET /crm/forms/{id}", s.requireUser(s.crmFormEdit))
	mux.HandleFunc("POST /crm/forms/{id}", s.requireUser(s.crmFormSave))
	mux.HandleFunc("GET /crm/forms/{id}/results", s.requireUser(s.crmFormResults))
	mux.HandleFunc("GET /crm/types", s.requireUser(s.crmTypes))
	mux.HandleFunc("POST /crm/types", s.requireUser(s.crmTypeSave))
	mux.HandleFunc("POST /crm/bulk/{kind}", s.requireUser(s.crmBulk))
	mux.HandleFunc("GET /crm/export/{kind}", s.requireUser(s.crmExport))
	mux.HandleFunc("POST /crm/import/{kind}", s.requireUser(s.crmImport))
	mux.HandleFunc("POST /crm/{kind}/{id}/merge", s.requireUser(s.crmMerge))
	mux.HandleFunc("POST /crm/{kind}/{id}/requisites", s.requireUser(s.crmRequisite))
	mux.HandleFunc("GET /crm/activities", s.requireUser(s.crmActivities))
	mux.HandleFunc("GET /crm/analytics", s.requireUser(s.crmAnalytics))
	mux.HandleFunc("GET /crm/invoices", s.requireUser(s.crmInvoicesList))
	mux.HandleFunc("GET /crm/invoices/{id}", s.requireUser(s.crmInvoicePage))
	mux.HandleFunc("GET /crm/invoices/{id}/print", s.requireUser(s.crmInvoicePrint))
	mux.HandleFunc("POST /crm/invoices/{id}", s.requireUser(s.crmInvoiceAction))
	mux.HandleFunc("POST /crm/invoices/{id}/products", s.requireUser(s.crmInvoiceRows))
	mux.HandleFunc("POST /crm/seller", s.requireUser(s.crmSeller))
	mux.HandleFunc("POST /crm/{kind}/{id}/invoices", s.requireUser(s.crmInvoiceMake))
	mux.HandleFunc("POST /crm/deals/{id}/recurrence", s.requireUser(s.crmRecurrence))
	mux.HandleFunc("GET /crm/currencies", s.requireUser(s.crmCurrencies))
	mux.HandleFunc("POST /crm/currencies", s.requireUser(s.crmCurrencySave))
	// «Складской учёт»: stock documents, warehouses, the stock (stock.go)
	mux.HandleFunc("GET /store", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/store/docs", http.StatusSeeOther) })
	mux.HandleFunc("GET /store/docs", s.requireUser(s.stockDocs))
	mux.HandleFunc("GET /store/docs/{id}", s.requireUser(s.stockDocPage))
	mux.HandleFunc("POST /store/docs/{id}", s.requireUser(s.stockDocAction))
	mux.HandleFunc("GET /store/warehouses", s.requireUser(s.stockWarehouses))
	mux.HandleFunc("POST /store/warehouses/{id}", s.requireUser(s.stockWarehouseSave))
	mux.HandleFunc("GET /store/stock", s.requireUser(s.stockList))
	mux.HandleFunc("GET /store/settings", s.requireUser(s.stockSettings))
	mux.HandleFunc("POST /store/settings", s.requireUser(s.stockSettingsSave))
	mux.HandleFunc("GET /store/rights", s.requireUser(s.stockRoles))
	mux.HandleFunc("POST /store/rights/{id}", s.requireUser(s.stockRoleSave))
	mux.HandleFunc("GET /store/reports", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/store/reports/stock", http.StatusSeeOther) })
	mux.HandleFunc("GET /store/reports/{kind}", s.requireUser(s.stockReport))
	mux.HandleFunc("POST /crm/deals/{id}/shipment", s.requireUser(s.crmDealShipment))
	mux.HandleFunc("GET /crm/products", s.requireUser(s.crmProducts))
	mux.HandleFunc("POST /crm/products/{id}", s.requireUser(s.crmProductSave))
	mux.HandleFunc("POST /crm/products/{id}/delete", s.requireUser(s.crmProductDelete))
	mux.HandleFunc("GET /crm/products/export", s.requireUser(s.crmProductsExport))
	mux.HandleFunc("POST /crm/products/import", s.requireUser(s.crmProductsImport))
	mux.HandleFunc("GET /crm/products/settings", s.requireUser(s.crmCatalogSettings))
	mux.HandleFunc("POST /crm/product-units/{id}", s.requireUser(s.crmUnitSave))
	mux.HandleFunc("POST /crm/product-props/{id}", s.requireUser(s.crmPropSave))
	mux.HandleFunc("GET /crm/products/{id}/photo", s.requireUser(s.crmProductPhotoFile))
	mux.HandleFunc("POST /crm/product-sections/{id}", s.requireUser(s.crmSectionSave))
	mux.HandleFunc("POST /crm/product-sections/{id}/delete", s.requireUser(s.crmSectionDelete))
	mux.HandleFunc("POST /crm/{kind}/{id}/products", s.requireUser(s.crmProductRows))
	mux.HandleFunc("POST /crm/activities/{id}", s.requireUser(s.crmActivityAction))
	mux.HandleFunc("POST /crm/{kind}/{id}/activities", s.requireUser(s.crmActivityAdd))
	mux.HandleFunc("GET /crm/robots", s.requireUser(s.crmRobots))
	mux.HandleFunc("POST /crm/robots", s.requireUser(s.crmRobotAdd))
	mux.HandleFunc("POST /crm/robots/{id}/delete", s.requireUser(s.crmRobotDelete))
	mux.HandleFunc("POST /crm/triggers", s.requireUser(s.crmTriggerAdd))
	mux.HandleFunc("POST /crm/triggers/{id}/delete", s.requireUser(s.crmTriggerDelete))
	mux.HandleFunc("GET /crm/{kind}", s.requireUser(s.crmList))
	mux.HandleFunc("GET /crm/{kind}/{id}", s.requireUser(s.crmCard))
	mux.HandleFunc("POST /crm/{kind}/{id}", s.requireUser(s.crmSave))
	mux.HandleFunc("POST /crm/{kind}/{id}/delete", s.requireUser(s.crmDelete))
	mux.HandleFunc("POST /crm/{kind}/{id}/comment", s.requireUser(s.crmComment))
	mux.HandleFunc("POST /crm/{kind}/{id}/mail", s.requireUser(s.crmMailSend))
	mux.HandleFunc("GET /crm/{kind}/{id}/mail-template/{tpl}", s.requireUser(s.crmMailFill))
	mux.HandleFunc("GET /crm/mail-templates", s.requireUser(s.crmMailTemplatesPage))
	mux.HandleFunc("GET /marketing", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/marketing/campaigns", http.StatusFound)
	})
	mux.HandleFunc("GET /marketing/campaigns", s.requireUser(s.mktCampaignsPage))
	mux.HandleFunc("GET /marketing/campaigns/{id}", s.requireUser(s.mktCampaignPage))
	mux.HandleFunc("POST /marketing/campaigns/{id}", s.requireUser(s.mktCampaignSave))
	mux.HandleFunc("POST /marketing/campaigns/{id}/{action}", s.requireUser(s.mktCampaignAction))
	mux.HandleFunc("GET /marketing/segments", s.requireUser(s.mktSegmentsPage))
	mux.HandleFunc("POST /marketing/segments/{id}", s.requireUser(s.mktSegmentSave))
	mux.HandleFunc("POST /marketing/segments/{id}/delete", s.requireUser(s.mktSegmentDelete))
	mux.HandleFunc("GET /marketing/stoplist", s.requireUser(s.mktStopPage))
	mux.HandleFunc("POST /marketing/stoplist", s.requireUser(s.mktStopChange))
	mux.HandleFunc("GET /marketing/ads", s.requireUser(s.mktAdsPage))
	mux.HandleFunc("POST /marketing/ads/sources/{id}", s.requireUser(s.mktAdSourceSave))
	mux.HandleFunc("POST /marketing/ads/expenses", s.requireUser(s.mktExpenseAdd))
	mux.HandleFunc("POST /marketing/ads/expenses/{id}/delete", s.requireUser(s.mktExpenseDelete))
	mux.HandleFunc("GET /marketing/analytics", s.requireUser(s.mktAnalytics))
	mux.HandleFunc("GET /m/u/{token}", s.mktUnsubscribe) // «Отписаться» of a campaign's letter: no sign-in
	mux.HandleFunc("POST /m/u/{token}", s.mktUnsubscribe)
	mux.HandleFunc("GET /m/o/{file}", s.mktOpened)   // the picture of a letter: opened
	mux.HandleFunc("GET /m/c/{token}", s.mktClicked) // a link of a letter: followed
	mux.HandleFunc("POST /crm/mail-templates/{id}", s.requireUser(s.crmMailTemplateSave))
	mux.HandleFunc("POST /crm/mail-templates/{id}/delete", s.requireUser(s.crmMailTemplateDelete))
	mux.HandleFunc("POST /crm/{kind}/{id}/timeline/{ev}/{act}", s.requireUser(s.crmEvent))
	mux.HandleFunc("POST /crm/{kind}/{id}/mail/{link}/unlink", s.requireUser(s.crmMailUnlink))
	mux.HandleFunc("POST /crm/{kind}/{id}/stage", s.requireUser(s.crmStage))
	mux.HandleFunc("POST /crm/leads/{id}/convert", s.requireUser(s.crmConvert))
	mux.HandleFunc("GET /mail", s.requireUser(s.mailPage))
	mux.HandleFunc("POST /mail", s.requireUser(s.mailAdd))
	mux.HandleFunc("POST /mail/{id}/delete", s.requireUser(s.mailRemove))
	mux.HandleFunc("POST /mail/{id}/sync", s.requireUser(s.mailSync))
	mux.HandleFunc("POST /mail/{id}/send", s.requireUser(s.mailSend))
	mux.HandleFunc("POST /mail/letters/{letter}/lead", s.requireUser(s.mailLead))
	mux.HandleFunc("GET /mail/letters/{letter}", s.requireUser(s.mailLetter))
	mux.HandleFunc("POST /mail/move", s.requireUser(s.mailMove))
	mux.HandleFunc("POST /mail/act", s.requireUser(s.mailAct))
	mux.HandleFunc("GET /mail/files", s.requireUser(s.mailFiles))
	mux.HandleFunc("POST /mail/folders/{id}/read", s.requireUser(s.mailReadAll))
	mux.HandleFunc("POST /mail/folders/{id}/empty", s.requireUser(s.mailEmpty))
	mux.HandleFunc("POST /mail/folders/{id}/import", s.requireUser(s.mailImport))
	mux.HandleFunc("POST /mail/categories", s.requireUser(s.mailCategorySave))
	mux.HandleFunc("POST /mail/categories/{id}/save", s.requireUser(s.mailCategorySave))
	mux.HandleFunc("POST /mail/categories/{id}/delete", s.requireUser(s.mailCategoryDelete))
	mux.HandleFunc("GET /mail/suggest", s.requireUser(s.mailSuggest))
	mux.HandleFunc("POST /mail/favorites", s.requireUser(s.mailFavSet))
	mux.HandleFunc("POST /mail/favorites/move", s.requireUser(s.mailFavMove))
	mux.HandleFunc("POST /mail/searches", s.requireUser(s.mailSearchSave))
	mux.HandleFunc("POST /mail/searches/{id}/save", s.requireUser(s.mailSearchSave))
	mux.HandleFunc("POST /mail/searches/{id}/delete", s.requireUser(s.mailSearchDelete))
	mux.HandleFunc("POST /mail/searches/recent/clear", s.requireUser(s.mailRecentClear))
	mux.HandleFunc("POST /mail/settings/focused", s.requireUser(s.mailFocusSave))
	mux.HandleFunc("POST /mail/settings/handling", s.requireUser(s.mailHandlingSave))
	mux.HandleFunc("POST /mail/settings/layout", s.requireUser(s.mailLayoutSave))
	mux.HandleFunc("POST /mail/settings/focused/rule", s.requireUser(s.mailFocusRule))
	mux.HandleFunc("POST /mail/rules", s.requireUser(s.mailRuleSave))
	mux.HandleFunc("POST /mail/rules/run", s.requireUser(s.mailRulesRun))
	mux.HandleFunc("POST /mail/rules/{id}/save", s.requireUser(s.mailRuleSave))
	mux.HandleFunc("POST /mail/rules/{id}/do", s.requireUser(s.mailRuleDo))
	mux.HandleFunc("POST /mail/quicksteps", s.requireUser(s.mailQuickSave))
	mux.HandleFunc("POST /mail/quicksteps/{id}/save", s.requireUser(s.mailQuickSave))
	mux.HandleFunc("POST /mail/quicksteps/{id}/do", s.requireUser(s.mailQuickChange))
	mux.HandleFunc("POST /mail/lists", s.requireUser(s.mailListSave))
	mux.HandleFunc("POST /mail/lists/{id}/save", s.requireUser(s.mailListSave))
	mux.HandleFunc("POST /mail/lists/{id}/delete", s.requireUser(s.mailListDelete))
	mux.HandleFunc("POST /mail/settings/ooo", s.requireUser(s.mailOOOSave))
	mux.HandleFunc("POST /mail/ooo/off", s.requireUser(s.mailOOOOff))
	mux.HandleFunc("GET /mail/sweep", s.requireUser(s.mailSweepPage))
	mux.HandleFunc("POST /mail/sweep", s.requireUser(s.mailSweep))
	mux.HandleFunc("POST /mail/sweeps/{id}/delete", s.requireUser(s.mailSweepDrop))
	mux.HandleFunc("POST /mail/{id}/folders", s.requireUser(s.mailFolderAdd))
	mux.HandleFunc("POST /mail/folders/{id}/rename", s.requireUser(s.mailFolderRename))
	mux.HandleFunc("POST /mail/folders/{id}/delete", s.requireUser(s.mailFolderDelete))
	mux.HandleFunc("GET /mail/compose", s.requireUser(s.mailComposeHTML))
	mux.HandleFunc("POST /mail/drafts/save", s.requireUser(s.mailDraftSave))
	mux.HandleFunc("POST /mail/drafts/send", s.requireUser(s.mailDraftSend))
	mux.HandleFunc("POST /mail/drafts/{id}/cancel", s.requireUser(s.mailDraftCancel))
	mux.HandleFunc("POST /mail/drafts/{id}/delete", s.requireUser(s.mailDraftDelete))
	mux.HandleFunc("POST /mail/drafts/{id}/files", s.requireUser(s.mailDraftFile))
	mux.HandleFunc("POST /mail/drafts/{id}/files/{file}/delete", s.requireUser(s.mailDraftFileDelete))
	mux.HandleFunc("GET /mail/rcpt", s.requireUser(s.mailRcpt))
	mux.HandleFunc("GET /mail/book", s.requireUser(s.mailBook))
	mux.HandleFunc("POST /mail/contacts", s.requireUser(s.mailContactSave))
	mux.HandleFunc("POST /mail/contacts/{id}/save", s.requireUser(s.mailContactSave))
	mux.HandleFunc("POST /mail/contacts/{id}/delete", s.requireUser(s.mailContactDelete))
	mux.HandleFunc("POST /mail/letters/{letter}/receipt", s.requireUser(s.mailReceipt))
	mux.HandleFunc("POST /mail/letters/{letter}/unsubscribe", s.requireUser(s.mailUnsubscribe))
	mux.HandleFunc("POST /mail/letters/{letter}/vote", s.requireUser(s.mailVote))
	mux.HandleFunc("POST /mail/letters/{letter}/recall", s.requireUser(s.mailRecall))
	mux.HandleFunc("GET /mail/settings", s.requireUser(s.mailSettingsPage))
	mux.HandleFunc("POST /mail/settings", s.requireUser(s.mailPrefsSave))
	mux.HandleFunc("POST /mail/settings/boxes/{id}", s.requireUser(s.mailBoxSignatures))
	mux.HandleFunc("POST /mail/settings/boxes/{id}/crm", s.requireUser(s.mailBoxCRM))
	mux.HandleFunc("POST /mail/settings/boxes/{id}/forward", s.requireUser(s.mailBoxForward))
	mux.HandleFunc("POST /mail/settings/boxes/{id}/senders", s.requireUser(s.mailBoxSenders))
	mux.HandleFunc("POST /mail/snippets/{kind}/new", s.requireUser(s.mailSnippetSave))
	mux.HandleFunc("POST /mail/snippets/{kind}/{id}/save", s.requireUser(s.mailSnippetSave))
	mux.HandleFunc("POST /mail/snippets/{kind}/{id}/delete", s.requireUser(s.mailSnippetDelete))
	s.mailMoreRoutes(mux) // files, notifications, printing, the source, senders
	// «Экспорт» of letters to a file (mailexport.go)
	s.mailExportRoutes(mux)
	// the mail with the calendar, tasks and the CRM; shared mailboxes (mailinvite.go, maillinks.go, mailshared.go)
	mux.HandleFunc("POST /mail/letters/{letter}/invite", s.requireUser(s.mailInviteAnswer))
	mux.HandleFunc("POST /mail/letters/{letter}/crm", s.requireUser(s.mailCRM))
	mux.HandleFunc("GET /mail/crm/search", s.requireUser(s.mailCRMSearch))
	mux.HandleFunc("POST /mail/letters/{letter}/share", s.requireUser(s.mailShare))
	mux.HandleFunc("GET /mail/share/rooms", s.requireUser(s.mailShareRooms))
	mux.HandleFunc("GET /mail/shared", s.requireUser(s.mailSharedIndex))
	mux.HandleFunc("POST /mail/shared", s.requireUser(s.mailShareSave))
	mux.HandleFunc("GET /mail/shared/{box}", s.requireUser(s.mailSharedPage))
	mux.HandleFunc("POST /mail/shared/{box}/people", s.requireUser(s.mailSharePerson))
	mux.HandleFunc("POST /mail/shared/{box}/unshare", s.requireUser(s.mailUnshare))
	mux.HandleFunc("POST /mail/shared/{box}/letters/{letter}/{do}", s.requireUser(s.mailSharedDo))
	mux.HandleFunc("GET /bp", s.requireUser(s.bpHome))
	mux.HandleFunc("GET /bp/start/{id}", s.requireUser(s.bpStartPage))
	mux.HandleFunc("POST /bp/start/{id}", s.requireUser(s.bpStart))
	mux.HandleFunc("GET /bp/i/{id}", s.requireUser(s.bpInstance))
	mux.HandleFunc("POST /bp/i/{id}/stop", s.requireUser(s.bpStop))
	mux.HandleFunc("POST /bp/i/{id}/delete", s.requireUser(s.bpDeleteInstance))
	mux.HandleFunc("POST /bp/approvals/{id}", s.requireUser(s.bpDecide))
	mux.HandleFunc("GET /bp/templates", s.requireUser(s.bpTemplates))
	mux.HandleFunc("GET /bp/templates/{id}", s.requireUser(s.bpTemplatePage))
	mux.HandleFunc("POST /bp/templates/{id}", s.requireUser(s.bpTemplateSave))
	mux.HandleFunc("POST /bp/templates/{id}/delete", s.requireUser(s.bpTemplateDelete))
	mux.HandleFunc("POST /bp/standard", s.requireUser(s.bpStandard))
	// «Виджет на сайт»: public, for visitors of a site (no go24 account)
	mux.HandleFunc("GET /w/{id}/widget.js", s.widgetScript)
	mux.HandleFunc("/w/{id}/api/{op}", s.widgetAPI)
	mux.HandleFunc("GET /openlines", s.requireUser(s.openlinesPage))
	mux.HandleFunc("POST /openlines/s/{id}", s.requireUser(s.openlinesAction))
	mux.HandleFunc("GET /openlines/live", s.requireUser(s.openlinesLive))
	mux.HandleFunc("GET /openlines/stats", s.requireUser(s.openlinesStats))
	mux.HandleFunc("GET /openlines/quick", s.requireUser(s.olQuickPage))
	mux.HandleFunc("POST /openlines/quick/{id}", s.requireUser(s.olQuickSave))
	mux.HandleFunc("POST /openlines/quick/{id}/delete", s.requireUser(s.olQuickDelete))
	mux.HandleFunc("GET /openlines/widgets", s.requireUser(s.widgetsPage))
	mux.HandleFunc("POST /openlines/widgets/{id}", s.requireUser(s.widgetSave))
	mux.HandleFunc("POST /openlines/widgets/{id}/delete", s.requireUser(s.widgetDelete))
	mux.HandleFunc("GET /openlines/widgets/{id}/preview", s.requireUser(s.widgetPreview))
	mux.HandleFunc("POST /client-error", s.requireUser(s.clientError)) // forms.js: an answer go24 did not mean
	mux.HandleFunc("GET /sites", s.requireUser(s.sitesHome))
	mux.HandleFunc("POST /sites", s.requireUser(s.siteCreate))
	mux.HandleFunc("GET /sites/{site}", s.requireUser(s.siteOpen))
	mux.HandleFunc("POST /sites/{site}/settings", s.requireUser(s.siteSettings))
	mux.HandleFunc("POST /sites/{site}/publish", s.requireUser(s.sitePublish))
	mux.HandleFunc("POST /sites/{site}/delete", s.requireUser(s.siteDelete))
	mux.HandleFunc("POST /sites/{site}/pages", s.requireUser(s.siteNewPage))
	mux.HandleFunc("POST /sites/{site}/files", s.requireUser(s.siteUpload))
	mux.HandleFunc("GET /sites/{site}/p/{page}", s.requireUser(s.sitePage))
	mux.HandleFunc("POST /sites/{site}/p/{page}", s.requireUser(s.siteSavePage))
	mux.HandleFunc("GET /sites/{site}/p/{page}/preview", s.requireUser(s.sitePreview))
	mux.HandleFunc("POST /sites/{site}/p/{page}/{act}", s.requireUser(s.sitePageAction))
	mux.HandleFunc("POST /sites/{site}/p/{page}/blocks", s.requireUser(s.siteAddBlock))
	mux.HandleFunc("POST /sites/{site}/p/{page}/blocks/{block}", s.requireUser(s.siteSaveBlock))
	mux.HandleFunc("POST /sites/{site}/p/{page}/blocks/{block}/{act}", s.requireUser(s.siteBlockAction))
	// published sites: for anyone, no sign-in
	mux.HandleFunc("GET /site/{slug}", s.sitePublic)
	mux.HandleFunc("GET /site/{slug}/{page}", s.sitePublic)
	mux.HandleFunc("POST /site/{slug}", s.siteFormSend)
	mux.HandleFunc("POST /site/{slug}/{page}", s.siteFormSend)
	mux.HandleFunc("GET /sitefile/{id}", s.siteFile)
	mux.HandleFunc("GET /bi", s.requireUser(s.biList))
	mux.HandleFunc("POST /bi", s.requireUser(s.biCreate))
	mux.HandleFunc("GET /bi/keys", s.requireUser(s.biKeys))
	mux.HandleFunc("POST /bi/keys", s.requireUser(s.biKeyCreate))
	mux.HandleFunc("POST /bi/keys/{id}/{act}", s.requireUser(s.biKeyAct))
	mux.HandleFunc("GET /bi/files", s.requireUser(s.biFiles))
	mux.HandleFunc("POST /bi/files", s.requireUser(s.biFileCreate))
	mux.HandleFunc("POST /bi/files/{fid}/{act}", s.requireUser(s.biFileAct))
	mux.HandleFunc("GET /bi/groups", s.requireUser(s.biGroups))
	mux.HandleFunc("POST /bi/groups", s.requireUser(s.biGroupCreate))
	mux.HandleFunc("POST /bi/groups/{gid}/{act}", s.requireUser(s.biGroupAct))
	mux.HandleFunc("GET /bi/{id}", s.requireUser(s.biDashboard))
	mux.HandleFunc("POST /bi/{id}", s.requireUser(s.biSave))
	mux.HandleFunc("POST /bi/{id}/copy", s.requireUser(s.biCopy))
	mux.HandleFunc("POST /bi/{id}/delete", s.requireUser(s.biDelete))
	mux.HandleFunc("POST /bi/{id}/publish", s.requireUser(s.biPublish))
	mux.HandleFunc("POST /bi/{id}/rights", s.requireUser(s.biRights))
	mux.HandleFunc("POST /bi/{id}/order", s.requireUser(s.biOrder))
	mux.HandleFunc("POST /bi/{id}/pin", s.requireUser(s.biPin))
	mux.HandleFunc("GET /bi/w/{wid}/csv", s.requireUser(s.biCSV))
	mux.HandleFunc("GET /crm/bi", s.requireUser(s.biSection("crm")))
	mux.HandleFunc("GET /tasks/bi", s.requireUser(s.biSection("tasks")))
	mux.HandleFunc("GET /company/bi", s.requireUser(s.biSection("company")))
	mux.HandleFunc("GET /bi/{id}/widget", s.requireUser(s.biWidgetPage))
	mux.HandleFunc("POST /bi/{id}/widget", s.requireUser(s.biWidgetSave))
	mux.HandleFunc("POST /bi/w/{wid}/{act}", s.requireUser(s.biWidgetAct))
	mux.HandleFunc("GET /booking", s.requireUser(s.bookingPage))
	mux.HandleFunc("POST /booking", s.requireUser(s.bookingSave))
	mux.HandleFunc("POST /booking/item/{id}", s.requireUser(s.bookingSave))
	mux.HandleFunc("POST /booking/item/{id}/status", s.requireUser(s.bookingStatus))
	mux.HandleFunc("POST /booking/item/{id}/delete", s.requireUser(s.bookingDelete))
	mux.HandleFunc("POST /booking/item/{id}/deal", s.requireUser(s.bookingDeal))
	mux.HandleFunc("POST /booking/item/{id}/wait", s.requireUser(s.bookingToWait))
	mux.HandleFunc("POST /booking/wait", s.requireUser(s.bookingWaitSave))
	mux.HandleFunc("POST /booking/wait/{id}", s.requireUser(s.bookingWaitSave))
	mux.HandleFunc("POST /booking/wait/{id}/book", s.requireUser(s.bookingWaitBook))
	mux.HandleFunc("POST /booking/wait/{id}/delete", s.requireUser(s.bookingWaitDelete))
	mux.HandleFunc("GET /booking/stats", s.requireUser(s.bookingStats))
	mux.HandleFunc("GET /booking/links", s.requireUser(s.bookingLinks))
	mux.HandleFunc("POST /booking/links", s.requireUser(s.bookingLinkSave))
	mux.HandleFunc("POST /booking/links/{id}", s.requireUser(s.bookingLinkSave))
	mux.HandleFunc("POST /booking/links/{id}/delete", s.requireUser(s.bookingLinkDelete))
	mux.HandleFunc("GET /booking/resources", s.requireUser(s.bookingResources))
	mux.HandleFunc("POST /booking/resources", s.requireUser(s.bookingResourceSave))
	mux.HandleFunc("POST /booking/resources/{id}", s.requireUser(s.bookingResourceSave))
	mux.HandleFunc("POST /booking/resources/{id}/delete", s.requireUser(s.bookingResourceDelete))
	mux.HandleFunc("POST /booking/types", s.requireUser(s.bookingTypeSave))
	mux.HandleFunc("POST /booking/types/{id}", s.requireUser(s.bookingTypeSave))
	mux.HandleFunc("POST /booking/types/{id}/delete", s.requireUser(s.bookingTypeDelete))
	mux.HandleFunc("GET /kb", s.requireUser(s.kbHome))
	mux.HandleFunc("POST /kb", s.requireUser(s.kbCreateSpace))
	mux.HandleFunc("GET /kb/trash", s.requireUser(s.kbTrash))
	mux.HandleFunc("POST /kb/templates/{id}/delete", s.requireUser(s.kbDeleteTemplate))
	mux.HandleFunc("GET /kb/{space}", s.requireUser(s.kbSpace))
	mux.HandleFunc("POST /kb/{space}/trash", s.requireUser(s.kbTrashAct))
	mux.HandleFunc("POST /kb/{space}/delete", s.requireUser(s.kbDeleteSpace))
	mux.HandleFunc("POST /kb/{space}/pages", s.requireUser(s.kbCreatePage))
	mux.HandleFunc("GET /kb/{space}/settings", s.requireUser(s.kbSettings))
	mux.HandleFunc("POST /kb/{space}/settings", s.requireUser(s.kbSaveSettings))
	mux.HandleFunc("POST /kb/{space}/rights", s.requireUser(s.kbRights))
	mux.HandleFunc("POST /kb/{space}/files", s.requireUser(s.kbUpload))
	mux.HandleFunc("POST /kb/{space}/preview", s.requireUser(s.kbPreview))
	mux.HandleFunc("POST /kb/{space}/publish", s.requireUser(s.kbPublish))
	mux.HandleFunc("POST /kb/{space}/mark", s.requireUser(s.kbMark))
	mux.HandleFunc("POST /kb/{space}/template", s.requireUser(s.kbSaveTemplate))
	mux.HandleFunc("GET /kb/{space}/export", s.requireUser(s.kbExport))
	mux.HandleFunc("POST /kb/{space}/import", s.requireUser(s.kbImport))
	mux.HandleFunc("POST /kb/{space}/{page}/{act}", s.requireUser(s.kbMovePage))
	mux.HandleFunc("GET /kb/{space}/{page}", s.requireUser(s.kbPage))
	mux.HandleFunc("POST /kb/{space}/{page}", s.requireUser(s.kbSavePage))
	mux.HandleFunc("POST /kb/{space}/{page}/delete", s.requireUser(s.kbDeletePage))
	mux.HandleFunc("GET /kbp/{token}", s.kbPublic) // «Публикация» of the knowledge base: no sign-in
	mux.HandleFunc("GET /kbp/{token}/{page}", s.kbPublic)
	mux.HandleFunc("GET /kbp/{token}/f/{id}", s.kbPublicFile)
	mux.HandleFunc("GET /im", s.requireUser(s.imPage))
	mux.HandleFunc("GET /im/messages", s.requireUser(s.imMessages))
	mux.HandleFunc("POST /im/send", s.requireUser(s.imSend))
	mux.HandleFunc("POST /im/react", s.requireUser(s.imReact))
	mux.HandleFunc("GET /im/direct", s.requireUser(s.imDirect))
	mux.HandleFunc("GET /im/notes", s.requireUser(s.imNotes))
	mux.HandleFunc("GET /im/live", s.requireUser(s.imLive))
	mux.HandleFunc("GET /im/unread", s.requireUser(s.imUnread))
	mux.HandleFunc("GET /presence", s.requireUser(s.presenceGet))
	mux.HandleFunc("POST /presence", s.requireUser(s.presenceSet))
	mux.HandleFunc("POST /im/read", s.requireUser(s.imRead))
	mux.HandleFunc("POST /im/readall", s.requireUser(s.imReadAll))
	mux.HandleFunc("POST /im/upload", s.requireUser(s.imUpload))
	mux.HandleFunc("POST /im/edit", s.requireUser(s.imEdit))
	mux.HandleFunc("POST /im/delete", s.requireUser(s.imDelete))
	mux.HandleFunc("POST /im/forward", s.requireUser(s.imForward))
	mux.HandleFunc("GET /im/file", s.requireUser(s.imFile))
	mux.HandleFunc("GET /im/file/preview", s.requireUser(s.imFilePreview))
	mux.HandleFunc("POST /im/transcribe", s.requireUser(s.imTranscribe))
	mux.HandleFunc("POST /im/group", s.requireUser(s.imGroup))
	mux.HandleFunc("POST /im/group/{action}", s.requireUser(s.imGroupAction))
	mux.HandleFunc("GET /im/photo", s.requireUser(s.imPhoto))
	mux.HandleFunc("GET /im/link", s.requireUser(s.imLink))
	mux.HandleFunc("GET /im/link/image", s.requireUser(s.imLinkImage))
	mux.HandleFunc("POST /im/pin", s.requireUser(s.imPin))
	mux.HandleFunc("GET /im/search", s.requireUser(s.imSearch))
	mux.HandleFunc("POST /im/typing", s.requireUser(s.imTyping))
	mux.HandleFunc("POST /im/mark", s.requireUser(s.imRoomMark))
	mux.HandleFunc("POST /im/autodelete", s.requireUser(s.imAutoDelete))
	mux.HandleFunc("POST /im/draft", s.requireUser(s.imDraft))
	mux.HandleFunc("POST /im/save", s.requireUser(s.imSave))
	mux.HandleFunc("POST /im/file/drive", s.requireUser(s.imFileDrive))
	mux.HandleFunc("POST /im/mail", s.requireUser(s.imMail))
	mux.HandleFunc("POST /im/remind", s.requireUser(s.imRemind))
	mux.HandleFunc("GET /im/saved", s.requireUser(s.imSaved))
	mux.HandleFunc("GET /im/media", s.requireUser(s.imMedia))
	mux.HandleFunc("GET /im/threads", s.requireUser(s.imThreads))
	mux.HandleFunc("GET /im/calls", s.requireUser(s.imCalls))
	mux.HandleFunc("POST /im/poll", s.requireUser(s.imPoll))
	mux.HandleFunc("POST /im/settings", s.requireUser(s.imSettingsSave))
	mux.HandleFunc("GET /im/export", s.requireUser(s.imExport))
	mux.HandleFunc("POST /im/poll/{action}", s.requireUser(s.imPollAction))
	mux.HandleFunc("POST /feed", s.requireUser(s.feedPost))
	mux.HandleFunc("POST /feed/{id}/comments", s.requireUser(s.feedComment))
	mux.HandleFunc("POST /feed/{id}/like", s.requireUser(s.feedLike))
	mux.HandleFunc("GET /feed/{id}/likes", s.requireUser(s.feedLikes))
	mux.HandleFunc("POST /feed/comments/{id}/like", s.requireUser(s.feedLike))
	mux.HandleFunc("GET /feed/comments/{id}/likes", s.requireUser(s.feedLikes))
	mux.HandleFunc("POST /feed/comments/{id}/edit", s.requireUser(s.feedCommentEdit))
	mux.HandleFunc("POST /feed/comments/{id}/delete", s.requireUser(s.feedCommentDelete))
	mux.HandleFunc("POST /feed/{id}/fav", s.requireUser(s.feedMark))
	mux.HandleFunc("POST /feed/{id}/pin", s.requireUser(s.feedMark))
	mux.HandleFunc("POST /feed/{id}/follow", s.requireUser(s.feedMark))
	mux.HandleFunc("POST /feed/{id}/hide", s.requireUser(s.feedMark))
	mux.HandleFunc("POST /feed/views", s.requireUser(s.feedSee))
	mux.HandleFunc("GET /feed/{id}/views", s.requireUser(s.feedViewers))
	mux.HandleFunc("POST /feed/{id}/vote", s.requireUser(s.feedVote))
	mux.HandleFunc("POST /feed/{id}/read", s.requireUser(s.feedRead))
	mux.HandleFunc("POST /feed/{id}/delete", s.requireUser(s.feedDelete))
	mux.HandleFunc("GET /feed/{id}/edit", s.requireUser(s.feedEditForm))
	mux.HandleFunc("POST /feed/{id}/edit", s.requireUser(s.feedEdit))
	mux.HandleFunc("POST /feed/{id}/share", s.requireUser(s.feedShare))
	mux.HandleFunc("GET /feed/live", s.requireUser(s.feedLive))
	mux.HandleFunc("GET /people", s.requireUser(s.people))
	mux.HandleFunc("GET /people/departments", s.requireUser(s.peopleDepartments))
	mux.HandleFunc("GET /notifications", s.requireUser(s.notifications))
	mux.HandleFunc("GET /settings/notifications", s.requireUser(s.notifySettings))
	mux.HandleFunc("POST /settings/notifications", s.requireUser(s.notifySettings))
	mux.HandleFunc("POST /settings/notifications/{channel}/link", s.requireUser(s.notifyLink))
	mux.HandleFunc("POST /settings/notifications/{channel}/unlink", s.requireUser(s.notifyUnlink))
	mux.HandleFunc("GET /settings/notify-bots", s.requireUser(s.notifyBots))
	mux.HandleFunc("POST /settings/notify-bots/{channel}", s.requireUser(s.notifyBotSave))
	mux.HandleFunc("GET /notifications/pop", s.requireUser(s.notifyPop))
	mux.HandleFunc("GET /notifications/live", s.requireUser(s.notifyLive))
	mux.HandleFunc("POST /notifications/read", s.requireUser(s.notifyRead))
	mux.HandleFunc("GET /notifications/{id}", s.requireUser(s.notifyOpen))
	mux.HandleFunc("GET /tasks", s.requireUser(s.allTasks))
	mux.HandleFunc("POST /tasks/presets", s.requireUser(s.presetSave))
	mux.HandleFunc("POST /tasks/bulk", s.requireUser(s.taskBulk))
	mux.HandleFunc("GET /tasks/export", s.requireUser(s.taskExport))
	mux.HandleFunc("POST /tasks/import", s.requireUser(s.taskImport))
	mux.HandleFunc("POST /tasks/presets/{id}/delete", s.requireUser(s.presetDelete))
	mux.HandleFunc("GET /tasks/new", s.requireUser(s.newTaskPage))
	mux.HandleFunc("GET /calendar", s.requireUser(s.calendarPage))
	mux.HandleFunc("GET /calendar/new", s.requireUser(s.eventPage))
	mux.HandleFunc("GET /calendar/busy", s.requireUser(s.eventBusy))
	mux.HandleFunc("GET /calendar/sync", s.requireUser(s.calendarSync))
	mux.HandleFunc("POST /calendar/sync", s.requireUser(s.calendarSyncSave))
	mux.HandleFunc("POST /calendar/import", s.requireUser(s.calendarImport))
	mux.HandleFunc("GET /calendar/settings", s.requireUser(s.calendarSettings))
	mux.HandleFunc("POST /calendar/settings", s.requireUser(s.calendarSettingsSave))
	mux.HandleFunc("GET /calendar/feed/{file}", s.calendarFeed)
	mux.HandleFunc("POST /calendar/new", s.requireUser(s.eventSave))
	mux.HandleFunc("POST /calendar/sections", s.requireUser(s.sectionSave))
	mux.HandleFunc("POST /calendar/sections/{id}/delete", s.requireUser(s.sectionDelete))
	mux.HandleFunc("POST /calendar/people", s.requireUser(s.personSave))
	mux.HandleFunc("POST /calendar/people/{id}/delete", s.requireUser(s.personDelete))
	mux.HandleFunc("POST /calendar/share", s.requireUser(s.shareSave))
	mux.HandleFunc("GET /calendar/slots", s.requireUser(s.calendarSlots))
	mux.HandleFunc("POST /calendar/slots", s.requireUser(s.calendarSlotSave))
	mux.HandleFunc("POST /calendar/slots/{id}/save", s.requireUser(s.calendarSlotSave))
	mux.HandleFunc("POST /calendar/slots/{id}/toggle", s.requireUser(s.calendarSlotToggle))
	mux.HandleFunc("POST /calendar/slots/{id}/delete", s.requireUser(s.calendarSlotDelete))
	mux.HandleFunc("GET /calendar/{id}", s.requireUser(s.eventPage))
	mux.HandleFunc("POST /calendar/{id}", s.requireUser(s.eventSave))
	mux.HandleFunc("POST /calendar/{id}/delete", s.requireUser(s.eventDelete))
	mux.HandleFunc("GET /calendar/ics/{id}", s.requireUser(s.eventICS))
	mux.HandleFunc("POST /calendar/{id}/move", s.requireUser(s.eventMove))
	mux.HandleFunc("POST /calendar/{id}/respond", s.requireUser(s.eventRespond))
	mux.HandleFunc("POST /calendar/{id}/files", s.requireUser(s.eventFiles))
	mux.HandleFunc("POST /calendar/{id}/files/{file}/remove", s.requireUser(s.eventFileRemove))
	mux.HandleFunc("POST /calendar/{id}/comments", s.requireUser(s.eventComment))
	mux.HandleFunc("GET /tasks/efficiency", s.requireUser(s.efficiencyPage))
	mux.HandleFunc("GET /flows", s.requireUser(s.flowsPage))
	mux.HandleFunc("GET /flows/new", s.requireUser(s.flowEdit))
	mux.HandleFunc("POST /flows/new", s.requireUser(s.flowSave))
	mux.HandleFunc("GET /flows/{id}", s.requireUser(s.flowPage))
	mux.HandleFunc("POST /flows/{id}/task", s.requireUser(s.flowTask))
	mux.HandleFunc("GET /flows/{id}/edit", s.requireUser(s.flowEdit))
	mux.HandleFunc("POST /flows/{id}/edit", s.requireUser(s.flowSave))
	mux.HandleFunc("POST /flows/{id}/active", s.requireUser(s.flowActive))
	mux.HandleFunc("POST /flows/{id}/delete", s.requireUser(s.flowDelete))
	mux.HandleFunc("POST /t/{key}/take", s.requireUser(s.taskTake))
	mux.HandleFunc("GET /tasks/roles", s.requireUser(s.taskRoles))
	mux.HandleFunc("POST /tasks/roles/{id}", s.requireUser(s.taskRoleSave))
	mux.HandleFunc("POST /tasks/roles/{id}/delete", s.requireUser(s.taskRoleDelete))
	mux.HandleFunc("POST /tasks/roles/{id}/members", s.requireUser(s.taskRoleMember))
	mux.HandleFunc("GET /tasks/trash", s.requireUser(s.trashPage))
	mux.HandleFunc("POST /tasks/trash/settings", s.requireUser(s.trashSettings))
	mux.HandleFunc("POST /tasks/trash/{id}/restore", s.requireUser(s.trashRestore))
	mux.HandleFunc("POST /tasks/trash/{id}/purge", s.requireUser(s.trashPurge))
	mux.HandleFunc("POST /t/{key}/delete", s.requireUser(s.taskDelete))
	mux.HandleFunc("GET /templates", s.requireUser(s.templatesPage))
	mux.HandleFunc("GET /templates/new", s.requireUser(s.templateEdit))
	mux.HandleFunc("POST /templates/new", s.requireUser(s.templateSave))
	mux.HandleFunc("GET /templates/{id}", s.requireUser(s.templateEdit))
	mux.HandleFunc("POST /templates/{id}", s.requireUser(s.templateSave))
	mux.HandleFunc("POST /templates/{id}/delete", s.requireUser(s.templateDelete))
	mux.HandleFunc("POST /templates/{id}/pause", s.requireUser(s.templatePause))
	mux.HandleFunc("POST /templates/{id}/run", s.requireUser(s.templateRun))
	mux.HandleFunc("POST /templates/{id}/access", s.requireUser(s.templateAccess))
	mux.HandleFunc("POST /tasks/new", s.requireUser(s.newTaskCreate))
	mux.HandleFunc("POST /p/{key}/settings", s.requireUser(s.projectSettings))
	mux.HandleFunc("GET /plan", s.requireUser(s.plan))
	mux.HandleFunc("GET /deadlines", s.requireUser(s.deadlines))
	mux.HandleFunc("POST /deadlines/move", s.requireUser(s.deadlineMove))
	mux.HandleFunc("POST /plan/move", s.requireUser(s.planMove))
	mux.HandleFunc("POST /plan/stages", s.requireUser(s.planStage))
	mux.HandleFunc("POST /admin/mode", s.requireUser(s.toggleAdminMode))
	mux.HandleFunc("POST /prefs/columns", s.requireUser(s.setColumns))
	mux.HandleFunc("GET /prefs/widths", s.requireUser(s.widths))
	mux.HandleFunc("POST /prefs/widths", s.requireUser(s.setWidths))
	mux.HandleFunc("GET /join/{token}", s.joinPage) // an invitation into a collab: also for people not signed in
	mux.HandleFunc("GET /join/{token}/signup", s.joinSignup)
	mux.HandleFunc("POST /join/{token}", s.requireUser(s.joinAccept))
	mux.HandleFunc("POST /p/{key}/guests", s.requireUser(s.guestInviteCreate))
	mux.HandleFunc("POST /p/{key}/collab-settings", s.requireUser(s.collabSettings))
	mux.HandleFunc("POST /p/{key}/delete", s.requireUser(s.collabDelete))
	mux.HandleFunc("POST /p/{key}/owner", s.requireUser(s.collabOwner))
	mux.HandleFunc("POST /p/{key}/guests/{id}/revoke", s.requireUser(s.guestInviteRevoke))
	mux.HandleFunc("GET /collabs", s.requireUser(s.collabsPage))
	mux.HandleFunc("POST /collabs", s.requireUser(s.collabCreate))
	mux.HandleFunc("POST /p/{key}/leave", s.requireUser(s.projectLeave))
	mux.HandleFunc("GET /projects", s.requireUser(s.projects))
	mux.HandleFunc("POST /projects", s.requireUser(s.createProject))
	mux.HandleFunc("GET /p/{key}", s.requireUser(s.project))
	mux.HandleFunc("POST /p/{key}/info", s.requireUser(s.projectInfo))
	mux.HandleFunc("POST /p/{key}/photo", s.requireUser(s.projectPhoto))
	mux.HandleFunc("GET /p/{key}/photo", s.requireUser(s.projectPhotoFile))
	mux.HandleFunc("POST /p/{key}/archive", s.requireUser(s.projectArchive))
	mux.HandleFunc("POST /p/{key}/invite", s.requireUser(s.inviteAnswer))
	mux.HandleFunc("POST /p/{key}/invites/{uid}/delete", s.requireUser(s.inviteCancel))
	mux.HandleFunc("POST /p/{key}/invite-perm", s.requireUser(s.invitePerm))
	mux.HandleFunc("GET /p/{key}/board", s.requireUser(s.board))
	mux.HandleFunc("POST /p/{key}/stages", s.requireUser(s.projectStages))
	mux.HandleFunc("GET /p/{key}/robots", s.requireUser(s.taskRobots))
	mux.HandleFunc("POST /p/{key}/robots", s.requireUser(s.taskRobotsDo))
	mux.HandleFunc("GET /p/{key}/scrum", s.requireUser(s.scrumPlan))
	mux.HandleFunc("GET /p/{key}/sprints", s.requireUser(s.sprintsDone))
	mux.HandleFunc("POST /p/{key}/sprints", s.requireUser(s.sprintsAction))
	mux.HandleFunc("POST /p/{key}/epics", s.requireUser(s.epicsAction))
	mux.HandleFunc("GET /p/{key}/dod", s.requireUser(s.scrumTeamPage))
	mux.HandleFunc("POST /p/{key}/dod", s.requireUser(s.scrumTeamAction))
	mux.HandleFunc("POST /t/{key}/dod", s.requireUser(s.taskDod))
	mux.HandleFunc("POST /t/{key}/sprint", s.requireUser(s.taskSprint))
	mux.HandleFunc("POST /t/{key}/points", s.requireUser(s.taskPoints))
	mux.HandleFunc("POST /t/{key}/epic", s.requireUser(s.taskEpic))
	mux.HandleFunc("POST /t/{key}/stage", s.requireUser(s.stageMove))
	mux.HandleFunc("GET /p/{key}/gantt", s.requireUser(s.gantt))
	mux.HandleFunc("POST /t/{key}/links", s.requireUser(s.linkAdd))
	mux.HandleFunc("POST /t/{key}/links/{id}/delete", s.requireUser(s.linkDelete))
	mux.HandleFunc("POST /p/{key}/tasks", s.requireUser(s.createTask))
	mux.HandleFunc("POST /p/{key}/members", s.requireUser(s.setMember))
	mux.HandleFunc("POST /p/{key}/members/{uid}/delete", s.requireUser(s.removeMember))
	mux.HandleFunc("GET /t/{key}", s.requireUser(s.task))
	mux.HandleFunc("POST /t/{key}/action", s.requireUser(s.taskAction))
	mux.HandleFunc("POST /t/{key}/edit", s.requireUser(s.taskEdit))
	mux.HandleFunc("POST /t/{key}/deadline", s.requireUser(s.taskDeadline))
	mux.HandleFunc("GET /settings", s.requireUser(s.settingsPage))
	mux.HandleFunc("POST /menu", s.requireUser(s.menuSave))
	mux.HandleFunc("POST /menu/reset", s.requireUser(s.menuReset))
	mux.HandleFunc("GET /settings/portal", s.requireUser(s.portalPage))
	mux.HandleFunc("POST /settings/portal", s.requireUser(s.portalSave))
	mux.HandleFunc("GET /portal/logo", s.portalLogo)
	mux.HandleFunc("GET /settings/events", s.requireUser(s.auditPage))
	mux.HandleFunc("GET /settings/admins", s.requireUser(s.adminsPage))
	mux.HandleFunc("POST /settings/admins", s.requireUser(s.adminsAdd))
	mux.HandleFunc("POST /settings/admins/{id}/remove", s.requireUser(s.adminsRemove))
	mux.HandleFunc("GET /settings/ip", s.requireUser(s.ipAccessPage))
	mux.HandleFunc("GET /settings/backup", s.requireUser(s.backupPage))
	mux.HandleFunc("POST /settings/backup", s.requireUser(s.backupSave))
	mux.HandleFunc("POST /settings/backup/run", s.requireUser(s.backupRun))
	mux.HandleFunc("POST /settings/backup/check", s.requireUser(s.backupCheck))
	mux.HandleFunc("GET /settings/backup/{id}/restore", s.requireUser(s.backupRestorePage))
	mux.HandleFunc("POST /settings/backup/{id}/restore", s.requireUser(s.backupRestore))
	mux.HandleFunc("POST /settings/ip", s.requireUser(s.ipAccessSave))
	mux.HandleFunc("POST /settings/ip/{id}/delete", s.requireUser(s.ipAccessSave))
	mux.HandleFunc("POST /settings/events", s.requireUser(s.auditPage))
	mux.HandleFunc("GET /settings/webhooks", s.requireUser(s.webhooksPage))
	mux.HandleFunc("GET /settings/apps", s.requireUser(s.appsSettings))
	mux.HandleFunc("POST /settings/apps/{id}", s.requireUser(s.appSave))
	mux.HandleFunc("GET /apps", s.requireUser(s.appsPage))
	mux.HandleFunc("GET /apps/{id}", s.requireUser(s.appPage))
	mux.HandleFunc("GET /apps/tab/{id}", s.requireUser(s.appTab))
	mux.HandleFunc("GET /oauth/authorize", s.requireUser(s.oauthAuthorize))
	mux.HandleFunc("GET /oauth/token", s.oauthToken)
	mux.HandleFunc("POST /oauth/token", s.oauthToken)
	mux.HandleFunc("POST /settings/webhooks/{id}", s.requireUser(s.webhookSave))
	mux.HandleFunc("GET /settings/worktime", s.requireUser(s.worktimePage))
	mux.HandleFunc("GET /settings/rooms", s.requireUser(s.roomsPage))
	mux.HandleFunc("GET /settings/calls", s.requireUser(s.callSettings))
	mux.HandleFunc("POST /settings/calls", s.requireUser(s.callSettings))
	mux.HandleFunc("POST /settings/call-services/{service}", s.requireUser(s.callServiceSave))
	mux.HandleFunc("GET /settings/ai", s.requireUser(s.aiPage))
	mux.HandleFunc("POST /settings/ai", s.requireUser(s.aiPage))
	mux.HandleFunc("POST /settings/ai/choice", s.requireUser(s.aiChoose))
	mux.HandleFunc("POST /settings/ai/{id}/delete", s.requireUser(s.aiDelete))
	mux.HandleFunc("POST /settings/ai/{id}/check", s.requireUser(s.aiCheck))
	mux.HandleFunc("GET /settings/call-links", s.requireUser(s.callLinksPage))
	mux.HandleFunc("POST /settings/call-links", s.requireUser(s.callLinksPage))
	mux.HandleFunc("GET /call/external", s.requireUser(s.callExternal))
	mux.HandleFunc("POST /call/external", s.requireUser(s.callExternal))
	mux.HandleFunc("GET /settings/collabs", s.requireUser(s.collabCreatorsSettings))
	mux.HandleFunc("POST /settings/collabs", s.requireUser(s.collabCreatorsSettings))
	mux.HandleFunc("GET /settings/telephony", s.requireUser(s.telephonySettings))
	mux.HandleFunc("POST /settings/telephony", s.requireUser(s.telephonySettings))
	mux.HandleFunc("POST /settings/telephony/number", s.requireUser(s.telephonyNumber))
	mux.HandleFunc("POST /telephony/trunks", s.requireUser(s.trunkSave))
	mux.HandleFunc("POST /telephony/trunks/{id}", s.requireUser(s.trunkSave))
	mux.HandleFunc("POST /telephony/trunks/{id}/delete", s.requireUser(s.trunkDelete))
	mux.HandleFunc("GET /telephony/trunks/{id}/greeting", s.requireUser(s.trunkGreeting))
	mux.HandleFunc("GET /phone", s.requireUser(s.phoneWindow))
	mux.HandleFunc("GET /telephony/ws", s.requireUser(s.phoneSocket))
	mux.HandleFunc("GET /telephony/live", s.requireUser(s.phoneLive))
	mux.HandleFunc("POST /telephony/cmd", s.requireUser(s.phoneCmd))
	mux.HandleFunc("POST /telephony/state", s.requireUser(s.phoneState))
	mux.HandleFunc("POST /telephony/cdr", s.phoneCDR) // Asterisk, with its key
	mux.HandleFunc("GET /telephony/calls", s.requireUser(s.phoneCalls))
	mux.HandleFunc("GET /telephony/calls/{id}/record", s.requireUser(s.phoneRecord))
	mux.HandleFunc("POST /telephony/calls/{id}/crm", s.requireUser(s.phoneCallCRM))
	mux.HandleFunc("POST /settings/rooms", s.requireUser(s.roomSave))
	mux.HandleFunc("POST /settings/rooms/{id}", s.requireUser(s.roomSave))
	mux.HandleFunc("POST /settings/rooms/{id}/delete", s.requireUser(s.roomDelete))
	mux.HandleFunc("GET /worktime", s.requireUser(s.timeman))
	mux.HandleFunc("POST /worktime/day", s.requireUser(s.workdayAction))
	mux.HandleFunc("GET /worktime/table", s.requireUser(s.worksheet))
	mux.HandleFunc("GET /worktime/d/{user}/{day}", s.requireUser(s.workdayPage))
	mux.HandleFunc("POST /worktime/d/{user}/{day}", s.requireUser(s.changeWorkday))
	mux.HandleFunc("POST /worktime/changes/{id}", s.requireUser(s.decideWorkday))
	mux.HandleFunc("GET /worktime/reports", s.requireUser(s.workReports))
	mux.HandleFunc("GET /worktime/reports/new", s.requireUser(s.newWorkReport))
	mux.HandleFunc("POST /worktime/reports", s.requireUser(s.sendWorkReport))
	mux.HandleFunc("GET /worktime/reports/{id}", s.requireUser(s.workReport))
	mux.HandleFunc("POST /worktime/reports/{id}/mark", s.requireUser(s.markWorkReport))
	mux.HandleFunc("POST /worktime/reports/{id}/comments", s.requireUser(s.commentWorkReport))
	mux.HandleFunc("GET /settings/{set}", s.requireUser(s.fieldsPage))
	mux.HandleFunc("POST /settings/{set}", s.requireUser(s.fieldAdd))
	mux.HandleFunc("POST /settings/{set}/{id}/delete", s.requireUser(s.fieldDelete))
	mux.HandleFunc("POST /settings/{set}/{id}/required", s.requireUser(s.fieldRequired))
	mux.HandleFunc("POST /u/{id}/fields", s.requireUser(s.userFields))
	mux.HandleFunc("GET /company", s.requireUser(s.employees))
	mux.HandleFunc("GET /company/structure", s.requireUser(s.structure))
	mux.HandleFunc("GET /company/changes", s.requireUser(s.staffChanges))
	mux.HandleFunc("GET /company/thanks", s.requireUser(s.thanksPage))
	mux.HandleFunc("GET /company/absences", s.requireUser(s.absencesPage))
	mux.HandleFunc("POST /company/absences", s.requireUser(s.absenceAdd))
	mux.HandleFunc("POST /company/absences/{id}", s.requireUser(s.absenceUpdate))
	mux.HandleFunc("GET /company/absences/report", s.requireUser(s.vacationReport))
	mux.HandleFunc("POST /company/vacation-remind-off", s.requireUser(s.vacationRemindOff))
	mux.HandleFunc("POST /company/absences/{id}/delete", s.requireUser(s.absenceDelete))
	mux.HandleFunc("GET /company/birthdays", s.requireUser(s.birthdaysPage))
	mux.HandleFunc("GET /company/invite", s.requireUser(s.invitePage))
	mux.HandleFunc("POST /company/invite", s.requireUser(s.inviteSend))
	mux.HandleFunc("POST /company/departments", s.requireUser(s.departments))
	mux.HandleFunc("GET /u/{id}", s.requireUser(s.profile))
	mux.HandleFunc("POST /u/{id}", s.requireUser(s.saveProfile))
	mux.HandleFunc("POST /u/{id}/telephony/password", s.requireUser(s.telephonyPassword))
	mux.HandleFunc("POST /u/{id}/departments", s.requireUser(s.userDepartments))
	mux.HandleFunc("POST /u/{id}/active", s.requireUser(s.userActive))
	mux.HandleFunc("POST /u/{id}/hr", s.requireUser(s.userHR))
	mux.HandleFunc("POST /u/{id}/employee", s.requireUser(s.userEmployee))
	mux.HandleFunc("POST /u/{id}/photo", s.requireUser(s.userPhoto))
	mux.HandleFunc("GET /u/{id}/photo", s.requireUser(s.photoFile))
	mux.HandleFunc("GET /u/{id}/card", s.requireUser(s.profileCard))
	mux.HandleFunc("POST /settings/worktime", s.requireUser(s.saveWorktime))
	mux.HandleFunc("POST /settings/worktime/reports", s.requireUser(s.saveReportRules))
	mux.HandleFunc("POST /t/{key}/participants", s.requireUser(s.taskParticipant))
	mux.HandleFunc("POST /t/{key}/delegate", s.requireUser(s.taskDelegate))
	mux.HandleFunc("POST /t/{key}/checklist", s.requireUser(s.checklistAdd))
	mux.HandleFunc("POST /t/{key}/checklist/{id}", s.requireUser(s.checklistChange))
	mux.HandleFunc("POST /t/{key}/checklists", s.requireUser(s.checklistList))
	mux.HandleFunc("POST /t/{key}/checklists/{id}", s.requireUser(s.checklistList))
	mux.HandleFunc("POST /t/{key}/comments", s.requireUser(s.commentAdd))
	mux.HandleFunc("GET /t/{key}/messenger", s.requireUser(s.taskMessenger))
	mux.HandleFunc("GET /t/{key}/live", s.requireUser(s.taskLive))
	mux.HandleFunc("POST /t/{key}/comments/{id}/react", s.requireUser(s.commentReact))
	mux.HandleFunc("POST /t/{key}/timer", s.requireUser(s.timerStart))
	mux.HandleFunc("POST /timer/stop", s.requireUser(s.timerStop))
	mux.HandleFunc("POST /t/{key}/time", s.requireUser(s.timeAdd))
	mux.HandleFunc("POST /t/{key}/time/{id}/delete", s.requireUser(s.timeDelete))
	mux.HandleFunc("POST /t/{key}/time/{id}", s.requireUser(s.timeUpdate))
	mux.HandleFunc("POST /t/{key}/mark", s.requireUser(s.taskMark))
	mux.HandleFunc("POST /t/{key}/tags", s.requireUser(s.taskTag))
	mux.HandleFunc("POST /t/{key}/rate", s.requireUser(s.taskRate))
	mux.HandleFunc("POST /t/{key}/reminders", s.requireUser(s.reminderAdd))
	mux.HandleFunc("POST /t/{key}/fields", s.requireUser(s.taskFields))
	mux.HandleFunc("POST /t/{key}/crm", s.requireUser(s.taskCRM))
	mux.HandleFunc("POST /t/{key}/reminders/{id}/delete", s.requireUser(s.reminderDelete))
	mux.HandleFunc("POST /t/{key}/comments/{id}/result", s.requireUser(s.taskResult))

	mux.HandleFunc("GET /settings/keys", s.requireUser(s.keys))
	mux.HandleFunc("POST /settings/keys", s.requireUser(s.createKey))
	mux.HandleFunc("POST /settings/keys/{id}/revoke", s.requireUser(s.revokeKey))

	s.apiRoutes(mux)

	mux.HandleFunc("GET /files", s.requireUser(s.files))
	mux.HandleFunc("GET /disk", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/disk/my", http.StatusFound) })
	mux.HandleFunc("GET /disk/recent", s.requireUser(s.diskRecent))
	mux.HandleFunc("GET /disk/shared", s.requireUser(s.diskShared))
	mux.HandleFunc("POST /disk/connect", s.requireUser(s.diskConnect))
	mux.HandleFunc("GET /docs", s.requireUser(s.docsPage))
	mux.HandleFunc("GET /boards", s.requireUser(s.boardsPage))
	for _, base := range []string{"/disk/{which}", "/disk/p/{key}", "/disk/sh/{id}", "/disk/u/{uid}"} {
		mux.HandleFunc("GET "+base, s.requireUser(s.diskPage))
		mux.HandleFunc("POST "+base+"/upload", s.requireUser(s.diskUpload))
		mux.HandleFunc("POST "+base+"/folders", s.requireUser(s.diskFolder))
		mux.HandleFunc("POST "+base+"/delete", s.requireUser(s.diskDelete))
		mux.HandleFunc("POST "+base+"/rename", s.requireUser(s.diskRename))
		mux.HandleFunc("POST "+base+"/lock", s.requireUser(s.diskLock))
		mux.HandleFunc("POST "+base+"/move", s.requireUser(s.diskMove))
		mux.HandleFunc("POST "+base+"/restore", s.requireUser(s.diskRestore))
		mux.HandleFunc("POST "+base+"/link", s.requireUser(s.diskLink))
		mux.HandleFunc("POST "+base+"/purge", s.requireUser(s.diskPurge))
		mux.HandleFunc("POST "+base+"/clean", s.requireUser(s.diskClean))
		mux.HandleFunc("POST "+base+"/version-restore", s.requireUser(s.diskVersionRestore))
		mux.HandleFunc("POST "+base+"/share", s.requireUser(s.diskShare))
		mux.HandleFunc("POST "+base+"/rights", s.requireUser(s.diskRights))
		mux.HandleFunc("GET "+base+"/edit/{file}", s.requireUser(s.officeEdit))
		mux.HandleFunc("POST "+base+"/new-doc", s.requireUser(s.officeNew))
		mux.HandleFunc("POST "+base+"/new-board", s.requireUser(s.boardNew))
	}
	for _, base := range []string{"/disk/{which}", "/disk/p/{key}", "/disk/sh/{id}", "/disk/u/{uid}", "/disk/sf/{sf}"} { // «Доски»
		mux.HandleFunc("GET "+base+"/board/{file}", s.requireUser(s.boardPage)) // ?scene — the scene itself
		mux.HandleFunc("POST "+base+"/board/{file}/save", s.requireUser(s.boardSave))
		mux.HandleFunc("POST "+base+"/board/{file}/pointer", s.requireUser(s.boardPointer))
	}
	// a file shared with the user by itself: it opens in the editor, or as the file
	mux.HandleFunc("GET /disk/sf/{sf}", s.requireUser(s.diskSharedFile))
	mux.HandleFunc("GET /disk/sf/{sf}/edit/{file}", s.requireUser(s.officeEdit))
	if s.Office != nil { // the document server's side of «Документы онлайн»: its links are signed, not signed in
		mux.HandleFunc("GET /onlyoffice/file/{id}/{stamp}", s.officeFile)
		mux.HandleFunc("GET /onlyoffice/convert/{id}/{stamp}", s.officeConvertFile)
		mux.HandleFunc("GET /onlyoffice/invoice/{id}/{lang}/{stamp}", s.officeInvoice)
		mux.HandleFunc("POST /onlyoffice/callback/{id}", s.officeCallback)
		mux.HandleFunc("GET /onlyoffice/tpl/{id}/{stamp}", s.officeTemplateFile)
		mux.HandleFunc("POST /onlyoffice/tplsave/{id}", s.officeTemplateSave)
		proxy := s.officeProxy()
		mux.HandleFunc("/office/", s.requireUser(func(w http.ResponseWriter, r *http.Request, _ auth.Session) { proxy.ServeHTTP(w, r) }))
	}
	mux.HandleFunc("POST /files", s.requireUser(s.upload))
	mux.HandleFunc("GET /disk/pick", s.requireUser(s.mailDrive)) // «Файл с Диска» of tasks and posts
	mux.HandleFunc("/dav/", s.dav)                               // the drives as a network disk: signed in with an API key
	mux.HandleFunc("/dav", s.dav)
	mux.HandleFunc("/caldav/", s.caldav) // the calendars over CalDAV: signed in with an API key
	mux.HandleFunc("/caldav", s.caldav)
	mux.HandleFunc("/.well-known/caldav", s.wellKnownCalDAV)
	mux.HandleFunc("POST /t/{key}/files/drive", s.requireUser(s.taskDriveFile))
	mux.HandleFunc("GET /files/{id}", s.requireUser(s.download))
	mux.HandleFunc("GET /files/{id}/preview", s.requireUser(s.filePreview))
	mux.HandleFunc("GET /s/{token}", s.sharePage) // public links of «Диск»: no sign-in
	mux.HandleFunc("POST /s/{token}", s.sharePassword)
	mux.HandleFunc("GET /s/{token}/f/{id}", s.shareFile)
	mux.HandleFunc("GET /s/{token}/zip", s.shareZip)
	mux.HandleFunc("GET /files/{id}/raw", s.requireUser(s.fileRaw))
	mux.HandleFunc("GET /files/{id}/versions", s.requireUser(s.fileVersions))
	mux.HandleFunc("GET /files/{id}/versions/{v}", s.requireUser(s.fileVersion))
	mux.HandleFunc("GET /files/{id}/about", s.requireUser(s.fileCard)) // «Подробнее»: the card with «Обсуждение»
	mux.HandleFunc("POST /files/{id}/comments", s.requireUser(s.fileComment))
	mux.HandleFunc("POST /files/{id}/drive", s.requireUser(s.saveToDrive)) // «Сохранить на Диск» of an attachment
	mux.HandleFunc("GET /settings/storages", s.requireUser(s.storagesPage))
	mux.HandleFunc("GET /settings/disk", s.requireUser(s.diskLimitsPage))
	mux.HandleFunc("POST /settings/disk", s.requireUser(s.saveDiskLimits))
	mux.HandleFunc("POST /settings/storages", s.requireUser(s.createStorage))
	mux.HandleFunc("POST /settings/storages/{id}/check", s.requireUser(s.checkStorage))
	mux.HandleFunc("POST /settings/storages/{id}/delete", s.requireUser(s.deleteStorage))
	return formAware(mux)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.DB.Ping(r.Context()); err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	fmt.Fprintln(w, "ok")
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	if sess, err := s.Sessions.Get(r); err == nil {
		http.Redirect(w, r, s.landing(r, sess.UserID), http.StatusFound)
		return
	}
	s.render(w, r, "login", page{TitleKey: "title.login"})
}

// ---------- auth ----------

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.OIDC == nil {
		http.Error(w, "Authentik is not configured (GO24_OIDC_*)", http.StatusServiceUnavailable)
		return
	}
	state := randomHex(16)
	authURL, err := s.OIDC.AuthCodeURL(r.Context(), state)
	if err != nil {
		s.fail(w, "oidc", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: state, Path: "/auth", MaxAge: 600,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(stateCookie)
	if err != nil || c.Value == "" || c.Value != r.URL.Query().Get("state") {
		http.Error(w, "invalid login state, try again", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: "", Path: "/auth", MaxAge: -1})

	claims, err := s.OIDC.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		s.fail(w, "oidc exchange", err)
		return
	}
	join := s.joinToken(r)
	userID, active, guest, err := s.signIn(r.Context(), claims, join)
	if err != nil {
		s.fail(w, "upsert user", err)
		return
	}
	s.Matrix.Wake() // a new employee (or a new login name) gets their Matrix account
	if !active {    // уволен: dismissed in «Сотрудники»
		s.Log.Info("login refused: dismissed", "user_id", userID)
		s.audit(r, audit.Event{Kind: "login_denied", UserID: userID})
		http.Error(w, s.tr(r, "err.dismissed"), http.StatusForbidden)
		return
	}
	admin := !guest && slices.ContainsFunc(claims.Groups, func(g string) bool { return slices.Contains(s.AdminGroups, g) })
	if err := s.Sessions.Set(w, auth.Session{UserID: userID, Email: claims.Email, Name: claims.Name, Admin: admin}); err != nil {
		s.fail(w, "session", err)
		return
	}
	s.audit(r, audit.Event{Kind: "login", UserID: userID})
	if join != "" { // come by an invitation into a collab: straight into it
		s.joinAfterLogin(w, r, join, userID)
		return
	}
	if guest {
		http.Redirect(w, r, guestLanding, http.StatusFound)
		return
	}
	if c, err := r.Cookie(backCookie); err == nil {
		http.SetCookie(w, &http.Cookie{Name: backCookie, Value: "", Path: "/auth", MaxAge: -1})
		if back, err := url.QueryUnescape(c.Value); err == nil && localPath(back) {
			http.Redirect(w, r, back, http.StatusFound)
			return
		}
	}
	http.Redirect(w, r, "/my", http.StatusFound)
}

// localPath: a page of go24 (from the root, not «//host» or «/\host»), safe to send the browser to.
func localPath(p string) bool {
	return strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//") && !strings.HasPrefix(p, "/\\") && !strings.ContainsAny(p, "\r\n")
}

// signIn finds (or makes) the go24 user of an Authentik login. join is the invitation into a collab the browser came
// by ("" — none).
func (s *Server) signIn(ctx context.Context, claims auth.Claims, join string) (userID string, active, guest bool, err error) {
	if claims.Groups == nil {
		claims.Groups = []string{} // users.groups is NOT NULL
	}
	// An employee imported signs in for the first time: take over the imported record
	// (matched by e-mail), so their tasks and comments become theirs instead of creating a new user.
	if claims.Email != "" {
		if _, err := s.DB.Exec(ctx, `
			UPDATE users SET oidc_sub = $1
			WHERE oidc_sub LIKE 'legacy:%' AND lower(email) = lower($2)
			  AND NOT EXISTS (SELECT 1 FROM users WHERE oidc_sub = $1)`, claims.Sub, claims.Email); err != nil {
			return "", false, false, fmt.Errorf("link imported user: %w", err)
		}
	}
	// A guest of collabs: in a guests' group of Authentik (they signed up by the guests' flow), or new to go24 and
	// come by a working invitation into a collab. It stays: no later login makes a guest an employee.
	inGuestGroup := slices.ContainsFunc(claims.Groups, func(g string) bool { return slices.Contains(s.GuestGroups, g) })
	invited := false
	if join != "" {
		_, jerr := s.Tasks.GuestInviteByToken(ctx, join)
		invited = jerr == nil
	}
	err = s.DB.QueryRow(ctx, `
		INSERT INTO users (oidc_sub, email, name, groups, username, guest) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (oidc_sub) DO UPDATE
		SET email = EXCLUDED.email, groups = EXCLUDED.groups, last_login_at = now(), username = EXCLUDED.username,
		    -- the name from Authentik until the employee fills in their card (ФИО) in go24
		    name = CASE WHEN users.first_name = '' AND users.last_name = '' THEN EXCLUDED.name ELSE users.name END,
		    guest = users.guest OR $7
		RETURNING id, active, guest`,
		claims.Sub, claims.Email, claims.Name, claims.Groups, claims.Username, inGuestGroup || invited, inGuestGroup,
	).Scan(&userID, &active, &guest)
	return userID, active, guest, err
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if sess, err := s.Sessions.Get(r); err == nil {
		s.audit(r, audit.Event{Kind: "logout", UserID: sess.UserID})
	}
	s.Sessions.Clear(w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) requireUser(next func(http.ResponseWriter, *http.Request, auth.Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.Sessions.Get(r)
		if err != nil {
			if r.Method == http.MethodGet && len(r.URL.RequestURI()) <= 2000 { // a link followed signed out opens after signing in
				http.SetCookie(w, &http.Cookie{Name: backCookie, Value: url.QueryEscape(r.URL.RequestURI()), Path: "/auth", MaxAge: 600,
					HttpOnly: true, SameSite: http.SameSiteLaxMode})
			}
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		// A dismissed employee loses access at once, not when the session cookie expires.
		var active, guest, stale, granted bool
		if s.DB != nil {
			if err := s.DB.QueryRow(r.Context(), `SELECT active, guest, seen_at IS NULL OR seen_at < now() - interval '1 minute', admin FROM users WHERE id = $1`,
				sess.UserID).Scan(&active, &guest, &stale, &granted); err != nil || !active {
				s.Sessions.Clear(w)
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			if !s.ipAllowed(r, sess.UserID) { // «Ограничение доступа по IP»: not from this address
				http.Error(w, s.tr(r, "ipaccess.denied", audit.ClientIP(r)), http.StatusForbidden)
				return
			}
			r = r.WithContext(tasks.WithViewer(r.Context(), sess.UserID, guest))
			if stale { // presence: «в сети»
				s.touchSeen(r.Context(), sess.UserID)
			}
		}
		if guest { // a guest sees their collabs only: the rest of go24 does not exist for them
			sess.Admin, sess.AdminMode = false, false
			if !guestPass(r, guestRoutes) {
				http.Error(w, s.tr(r, "err.not_found"), http.StatusNotFound)
				return
			}
		}
		if granted && !guest && !sess.Admin { // made an administrator in go24: at once, and no more once taken away
			sess.Admin, sess.Granted = true, true
		} else if !sess.Admin {
			sess.AdminMode = false
		}
		r = r.WithContext(withSession(r.Context(), sess))
		if sess.AdminActive() {
			// The task store widens visibility for this request only; see tasks.WithAdmin.
			r = r.WithContext(tasks.WithAdmin(r.Context()))
		}
		if s.frozen(r) { // a collab in the archive is read-only
			s.frozenBack(w, r)
			return
		}
		next(w, r, sess)
	}
}

// toggleAdminMode switches admin mode for administrators; every switch is logged.
func (s *Server) toggleAdminMode(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	sess.AdminMode = !sess.AdminMode
	if err := s.Sessions.Set(w, sess); err != nil {
		s.fail(w, "session", err)
		return
	}
	s.Log.Info("admin mode", "user_id", sess.UserID, "email", sess.Email, "on", sess.AdminMode)
	kind := "admin_off"
	if sess.AdminMode {
		kind = "admin_on"
	}
	s.audit(r, audit.Event{Kind: kind, UserID: sess.UserID})
	back := "/my"
	if ref, err := url.Parse(r.Referer()); err == nil && ref.Host == r.Host && ref.Path != "" {
		back = ref.RequestURI()
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// ---------- helpers ----------

// hasThemeCSS is checked per request, so a theme can be added or edited without a restart.
func (s *Server) hasThemeCSS() bool {
	if s.ThemeDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(s.ThemeDir, "theme.css"))
	return err == nil
}

func (s *Server) fail(w http.ResponseWriter, what string, err error) {
	s.Log.Error(what, "err", err)
	http.Error(w, "internal error: "+what, http.StatusInternalServerError)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

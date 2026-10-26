package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/kb"
	"go24/internal/matrixsync"
	"go24/internal/msgfmt"
	"go24/internal/office"
	"go24/internal/portal"
	"go24/internal/sites"
	"go24/internal/tasks"
)

//go:embed templates/*.html templates/parts/*.html
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

// iconNames lists the icons shipped in static/icons; the "icon" template func refuses unknown names.
var iconNames = func() map[string]bool {
	files, err := fs.Glob(staticFS, "static/icons/*.svg")
	if err != nil {
		panic(err)
	}
	out := map[string]bool{}
	for _, f := range files {
		out[strings.TrimSuffix(path.Base(f), ".svg")] = true
	}
	return out
}()

// assetVersion changes whenever a file in static/ changes; links carry it as ?v= so browsers that cached
// /static/ for a while still get the new CSS and scripts right after a deploy.
var assetVersion = func() string {
	h := fnv.New32a()
	err := fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := staticFS.ReadFile(p)
		h.Write([]byte(p))
		h.Write(b)
		return err
	})
	if err != nil {
		panic(err)
	}
	return strconv.FormatUint(uint64(h.Sum32()), 36)
}()

// staticHandler serves /static/ (CSS, icons) from the binary.
func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return cached(http.StripPrefix("/static/", http.FileServer(http.FS(sub))))
}

// themeHandler serves /theme/ from GO24_THEME_DIR: theme.css plus any assets it references (icons, fonts, logo).
func themeHandler(dir string) http.Handler {
	return cached(http.StripPrefix("/theme/", http.FileServer(http.Dir(dir))))
}

func cached(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		h.ServeHTTP(w, r)
	})
}

// initials: "Иванов Пётр" → "ИП".
func initials(name string) string {
	var out []rune
	for _, w := range strings.Fields(name) {
		r := []rune(w)
		if len(r) > 0 && unicode.IsLetter(r[0]) {
			out = append(out, unicode.ToUpper(r[0]))
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return string(out)
}

// avatar renders a colored circle with initials; the color is stable per name.
func avatar(name string, large bool) template.HTML {
	h := fnv.New32a()
	h.Write([]byte(name))
	hue := h.Sum32() % 360
	cls := "ava"
	if large {
		cls += " lg"
	}
	// Only the hue is chosen here; saturation, lightness and text color come from CSS tokens.
	return template.HTML(fmt.Sprintf(`<span class="%s" style="--h:%d" title="%s">%s</span>`,
		cls, hue, template.HTMLEscapeString(name), template.HTMLEscapeString(initials(name))))
}

// baseFuncs do not depend on the language.
var baseFuncs = template.FuncMap{
	// menuLabels are the texts menu.js needs (menu.<key>) for «Настроить меню»
	"menuLabels": func() []string { return menuLabels },
	// asset is a /static/ URL with the build's version, e.g. {{asset "css/app.css"}}.
	"asset": func(name string) string { return "/static/" + name + "?v=" + assetVersion },
	// icon renders <i class="i i-NAME">; the picture itself is set in CSS (icons.css or a theme).
	"icon": func(name string) (template.HTML, error) {
		if !iconNames[name] {
			return "", fmt.Errorf("unknown icon %q", name)
		}
		return template.HTML(`<i class="i i-` + name + `" aria-hidden="true"></i>`), nil
	},
	"pct": func(done, total int) int {
		if total == 0 {
			return 0
		}
		return done * 100 / total
	},
	"actionIcon": func(a tasks.Action) string {
		return map[tasks.Action]string{
			tasks.ActionStart: "player-play", tasks.ActionPause: "player-pause", tasks.ActionComplete: "check",
			tasks.ActionApprove: "circle-check", tasks.ActionDisapprove: "arrow-back-up",
			tasks.ActionDefer: "hourglass-high", tasks.ActionRenew: "arrow-back-up",
		}[a]
	},
	"ava": func(name string) template.HTML { return avatar(name, false) },
	// photo is the employee picture: one go24 image for everyone (set in CSS, --avatar-photo).
	// An uploaded photo replaces it (url from Person/Profile.PhotoURL).
	"photo": func(name, url string, large bool) template.HTML {
		cls := "ava photo"
		if large {
			cls += " lg"
		}
		style := ""
		if url != "" {
			style = ` style="background-image:url('` + template.HTMLEscapeString(url) + `')"`
		}
		return template.HTML(`<span class="` + cls + `"` + style + ` role="img" aria-label="` + template.HTMLEscapeString(name) + `" title="` + template.HTMLEscapeString(name) + `"></span>`)
	},
	"avaLg": func(name string) template.HTML { return avatar(name, true) },
	"person": func(name string) template.HTML {
		return template.HTML(`<span class="person">`) + avatar(name, false) + template.HTML(template.HTMLEscapeString(name)+`</span>`)
	},
	// dtinput formats a deadline for <input type="datetime-local"> (fixed format, not localized).
	"dtinput": func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.Local().Format("2006-01-02T15:04")
	},
	"statusClass": func(s tasks.Status) string { return "st-" + string(s) },
	"statusIcon": func(s tasks.Status) string {
		return map[tasks.Status]string{
			tasks.StatusPending: "hourglass-high", tasks.StatusInProgress: "player-play", tasks.StatusAwaitingControl: "eye",
			tasks.StatusCompleted: "circle-check", tasks.StatusDeferred: "player-pause",
		}[s]
	},
	// clock shows the time of day of a chat message.
	"clock": func(t time.Time) string { return t.Local().Format("15:04") },
	"now":   time.Now,
	// hms shows seconds as hours and minutes of a working day, "8:05"
	"hms":        func(secs int) string { return fmt.Sprintf("%d:%02d", secs/3600, secs/60%60) },
	"md":         kb.Render, // a knowledge base page
	"mark":       kb.Mark,   // a found snippet with the query marked
	"money":      crm.Amount,
	"bookingSum": bookingSum,
	"mktROI":     mktROI,
	"keepOf":     keepOf,
	"photoURL":   company.PhotoURL,
	"sitefields": func() []string { return sites.FormFields }, // the fields a site's form may ask
	// dict builds a map for passing several values to a sub-template: dict "P" $p "On" "board".
	"dict": func(kv ...any) (map[string]any, error) {
		if len(kv)%2 != 0 {
			return nil, fmt.Errorf("dict: odd arguments")
		}
		m := map[string]any{}
		for i := 0; i < len(kv); i += 2 {
			k, ok := kv[i].(string)
			if !ok {
				return nil, fmt.Errorf("dict: key %v", kv[i])
			}
			m[k] = kv[i+1]
		}
		return m, nil
	},
	"fpts": func(p *float64) string {
		if p == nil {
			return ""
		}
		return strconv.FormatFloat(*p, 'f', -1, 64)
	},
	"fnum":      func(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) },
	"ptr":       func(t time.Time) *time.Time { return &t },
	"deref":     func(t *time.Time) time.Time { return *t },
	"hasPrefix": strings.HasPrefix,
	"join":      strings.Join,
	"mailGroup": func(t time.Time, pinned bool) string { return mailGroup(t, pinned, time.Now()) },
	// indent shifts a department name in a <select> by its depth in the structure.
	"indent": func(depth int) string { return strings.Repeat("\u00a0\u00a0\u00a0", depth) },
	// preview: the start of a text on one line (a letter in the list)
	"preview": func(text string) string {
		r := []rune(strings.Join(strings.Fields(text), " "))
		if len(r) > 160 {
			return string(r[:160]) + "…"
		}
		return string(r)
	},
	// deptCtx passes a structure node and the page data to the recursive "dept" template.
	"deptCtx": func(n *company.Department, data any) map[string]any { return map[string]any{"Node": n, "Data": data} },
	// tl bundles a task list with the user's columns; gear adds the column picker to the header.
	"tl": func(list []tasks.Task, cols map[string]bool, gear bool) taskList {
		if cols == nil {
			cols = map[string]bool{}
			for _, c := range tasks.DefaultColumns {
				cols[c] = true
			}
		}
		return taskList{Tasks: list, Cols: cols, All: tasks.ListColumns, Gear: gear}
	},
	// hm renders optional minutes as "1:30" for an input field.
	"hm": func(m *int) string {
		if m == nil {
			return ""
		}
		return fmt.Sprintf("%d:%02d", *m/60, *m%60)
	},
	// hhmm renders minutes of the day as "09:30" (a time field, «Онлайн-запись»).
	"hhmm": func(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) },
	// nl2br shows a text with its line breaks; "@Name" of an employee is shown as a mention.
	"nl2br": func(s string) template.HTML {
		return template.HTML(strings.ReplaceAll(markMentions(template.HTMLEscapeString(s)), "\n", "<br>"))
	},
	// feedtext is nl2br of a feed post, its #tags are links to the feed of the tag.
	"feedtext": func(s string) template.HTML {
		return template.HTML(strings.ReplaceAll(markHashtags(markMentions(template.HTMLEscapeString(s))), "\n", "<br>"))
	},
	// imtext shows a chat message formatted as in the portal messenger: bold, quotes, code, clickable links, mentions.
	"imtext": func(s string) template.HTML { return template.HTML(markMentions(msgfmt.HTML(s))) },
	// implain: a chat message without its formatting marks, for a one-line preview
	"implain": msgfmt.Plain,
	"list":    func(v ...string) []string { return v },
	"secs":    func(minutes int) int { return minutes * 60 },
	"add":     func(a, b int) int { return a + b },
	"share":   share,
	"mul":     func(a, b int) int { return a * b },
	"half":    func(a int) int { return a / 2 },
	// dinput formats a plan date for <input type="date">.
	"dinput": func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.Format("2006-01-02")
	},
	"quickReactions": func() []string { return tasks.QuickReactions },
	// browser: «Chrome 130 · Windows» of a User-Agent («Журнал событий»)
	"browser": audit.Browser,
	// upick: the people picker's data (upick.go)
	"upick": newUserPick,
	// persons: company people as task-form users (for the people picker)
	"persons": func(list []company.Person) []tasks.User {
		out := make([]tasks.User, len(list))
		for i, p := range list {
			out[i] = tasks.User{ID: p.ID, Name: p.Name}
		}
		return out
	},
	// ifs: a if cond else b
	"ifs": func(cond bool, a, b string) string {
		if cond {
			return a
		}
		return b
	},
	// dur2hm: seconds as H:MM for a duration field
	"dur2hm": func(secs int) string { m := (secs + 30) / 60; return fmt.Sprintf("%d:%02d", m/60, m%60) },
	// wdnum: 1 = Monday … 7 = Sunday
	"wdnum": func(t time.Time) int { return (int(t.Weekday())+6)%7 + 1 },
	// hasInt: is the number (as text, from a template list) in the list
	"hasInt": func(list []int, v string) bool { return slices.Contains(list, atoi(v)) },
	"has":    func(list []string, v string) bool { return slices.Contains(list, v) },
	"contains": func(users []tasks.User, id string) bool {
		for _, u := range users {
			if u.ID == id {
				return true
			}
		}
		return false
	},
	// json renders a value for a data-* attribute; nil maps and slices become {} so JS can index them.
	"json": func(v any) (string, error) {
		b, err := json.Marshal(v)
		if string(b) == "null" {
			return "{}", err
		}
		return string(b), err
	},
}

// langFuncs bind translation and date formatting to one language.
func langFuncs(b *i18n.Bundle, lang string) template.FuncMap {
	tr := func(key string, args ...any) string { return b.T(lang, key, args...) }
	format := func(t time.Time) string { return t.Local().Format(tr("format.datetime")) }
	return template.FuncMap{
		"t": tr,
		// guestMark is the mark «гость» beside the name of a guest of collabs (by the person's id)
		"guestMark": func(id string) template.HTML {
			if m := guestIDs.Load(); m == nil || !(*m)[id] {
				return ""
			}
			return template.HTML(`<span class="pill guest">` + template.HTMLEscapeString(tr("collab.guest")) + `</span>`)
		},
		"date": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return format(*t)
		},
		"datetime":       format,
		"crmCall":        crmCall, // «Звонок» in a CRM card's timeline
		"crmObserverIDs": crm.ObserverIDs,
		// crmStage shows a stage kept in a CRM timeline ("code:won" → its name; "a → b" both sides)
		"crmStage": func(v string) string {
			parts := strings.Split(v, " → ")
			for i, p := range parts {
				if c, ok := strings.CutPrefix(p, "code:"); ok {
					parts[i] = tr("crm.stage." + c)
				}
			}
			return strings.Join(parts, " → ")
		},
		// crmPipe shows a change of pipeline kept in a CRM timeline ("code:default → Тендеры")
		"crmPipe": func(v string) string {
			parts := strings.Split(v, " → ")
			for i, p := range parts {
				if c, ok := strings.CutPrefix(p, "code:"); ok {
					parts[i] = tr("crm.pipeline." + c)
				}
			}
			return strings.Join(parts, " → ")
		},
		// crmChanges shows an edit kept in a CRM timeline: each changed field «Сумма: 100 → 200» on its line
		"crmChanges": func(body string) template.HTML {
			value := func(field, v string) string {
				switch {
				case v == "":
					return "—"
				case strings.HasPrefix(v, "date:"):
					if d, err := time.Parse(time.DateOnly, v[5:]); err == nil {
						return d.Format(tr("format.date"))
					}
				case strings.HasPrefix(v, "code:") && field == "stage":
					return tr("crm.stage." + v[5:])
				case strings.HasPrefix(v, "code:") && !strings.HasPrefix(field, "uf:"):
					return tr("crm.dict." + v[5:])
				}
				return v
			}
			var out strings.Builder
			for _, c := range crm.Changes(body) {
				name, own := strings.CutPrefix(c.Field, "uf:")
				if !own {
					name = tr("crm.f." + c.Field)
				}
				out.WriteString(`<div class="crm-change">` + template.HTMLEscapeString(tr("crm.ev.changed", name)) + ` <s class="muted">` +
					template.HTMLEscapeString(value(c.Field, c.Old)) + `</s> → <b>` + template.HTMLEscapeString(value(c.Field, c.New)) + `</b></div>`)
			}
			return template.HTML(out.String())
		},
		// crmInvoice shows a quote's or an invoice's event kept in a CRM timeline ("invoice:3:paid" → «Счёт № 3: оплачен»,
		// "invoice:3:pay:500.00 RUB" → «Счёт № 3: оплата 500.00 RUB»)
		"crmInvoice": func(v string) string {
			p := strings.SplitN(v, ":", 3)
			if len(p) < 3 {
				return v
			}
			if sum, ok := strings.CutPrefix(p[2], "pay:"); ok { // a payment: "invoice:3:pay:500.00 RUB"
				return tr("crm.doc."+p[0]) + " № " + p[1] + ": " + tr("crm.inv_paid_ev", sum)
			}
			return tr("crm.doc."+p[0]) + " № " + p[1] + ": " + tr("crm.inv."+p[2])
		},
		// crmShip links the «Реализация» a won deal made by itself ("id|number" → «Реализация № 5» as a link)
		"crmShip": func(v string) template.HTML {
			id, number, _ := strings.Cut(v, "|")
			return template.HTML(`<a href="/store/docs/` + template.HTMLEscapeString(id) + `">` + template.HTMLEscapeString(tr("stock.kind.shipment")) +
				" № " + template.HTMLEscapeString(number) + `</a>`)
		},
		// crmRecur links the other deal of a schedule ("id|title" → «title» as a link)
		"crmRecur": func(v string) template.HTML {
			id, title, _ := strings.Cut(v, "|")
			return template.HTML(`<a href="/crm/deals/` + template.HTMLEscapeString(id) + `">«` + template.HTMLEscapeString(title) + `»</a>`)
		},
		// crmConv links the card a lead conversion line of the timeline names ("deal:id", "lead:id"; once a bare id —
		// the deal on a lead's card, the lead on a deal's)
		"crmConv": func(kind, v string) template.HTML {
			entity, id, ok := strings.Cut(v, ":")
			if !ok {
				entity, id = map[bool]string{true: "deal", false: "lead"}[kind == "leads"], v
			}
			p := map[string]string{"deal": "deals", "lead": "leads", "contact": "contacts", "company": "companies"}[entity]
			if p == "" {
				return ""
			}
			return template.HTML(`<a href="/crm/` + p + `/` + template.HTMLEscapeString(id) + `">` + template.HTMLEscapeString(tr("crm."+entity)) + `</a>`)
		},
		// crmActivity shows a done deed kept in a CRM timeline ("call:subject" → «Звонок: subject»)
		"crmActivity": func(v string) string {
			kind, subject, _ := strings.Cut(v, ":")
			return tr("crm.act."+kind) + ": " + subject
		},
		// crmRobot shows what a robot did in a CRM timeline ("mail:addr" → «отправил письмо addr», "fail:kind:err.key" → a warning)
		"crmRobot": func(v string) string {
			what, rest, _ := strings.Cut(v, ":")
			switch what {
			case "fail":
				kind, key, _ := strings.Cut(rest, ":")
				return "⚠ " + tr("robots.kind."+kind) + ": " + tr(key)
			case "activity":
				kind, subject, _ := strings.Cut(rest, ":")
				return tr("robots.made_activity") + " " + tr("crm.act."+kind) + ": " + subject
			case "mail", "responsible":
				return tr("robots.did."+what) + " " + rest
			case "trigger":
				return tr("robots.did.trigger") + " " + tr("triggers.kind."+rest)
			}
			return v
		},
		// size formats a file size: "840 Б", "1,2 МБ"
		"size":     func(n int64) string { return formatSize(tr, n) },
		"fileKind": fileKind,
		// officeKind: the document server opens the file (word, cell, slide, pdf); officeEditable — and edits it
		"officeKind":     office.Kind,
		"docKind":        docKind,
		"officeEditable": office.Editable,
		"isBoard":        drive.IsBoard, // «Доски»
		"ext":            func(name string) string { return strings.ToUpper(strings.TrimPrefix(path.Ext(name), ".")) },
		"mmss":           func(ms int) string { return fmt.Sprintf("%d:%02d", ms/60000, ms/1000%60) },
		// callDur is how long a call went on: "45 с", "12 мин"
		"callDur": func(secs int) string {
			if secs < 60 {
				return tr("call.dur_secs", secs)
			}
			return tr("call.dur_mins", (secs+30)/60)
		},
		"day": func(t time.Time) string { return t.Format(tr("format.date")) },
		// bday: a birthday as «1 августа» / «1 August», with the year («1 августа 1990») when year is set
		"bday": func(t time.Time, year bool) string {
			m := tr(fmt.Sprintf("month_gen.%d", int(t.Month())))
			if year {
				return tr("format.daymonthyear", t.Day(), m, t.Year())
			}
			return tr("format.daymonth", t.Day(), m)
		},
		// dur formats seconds as "2 ч 05 мин" / "2h 05m".
		"dur": func(secs int) string { return formatDuration(tr, secs) },
		// durs formats a time span to the second under a minute ("40 с"), as dur above it (the open lines' answer times).
		"durs": func(d time.Duration) string {
			if secs := int(d / time.Second); secs < 60 {
				return tr("time.s", secs)
			}
			return formatDuration(tr, int(d/time.Second))
		},
		// durm formats an optional number of minutes (estimates); "" when not set.
		"durm": func(m *int) string {
			if m == nil {
				return ""
			}
			return formatDuration(tr, *m*60)
		},
		// hv shows a history value: codes (status, priority, flags, timestamps) are translated or formatted;
		// anything else (names, titles, values written before i18n) is shown as stored.
		"hv": func(field, v string) string { return historyValue(b, lang, field, v) },
	}
}

// historyValue shows a history value: codes (status, priority, flags, timestamps) are translated or formatted;
// anything else (names, titles, values written before i18n) is shown as stored.
func historyValue(b *i18n.Bundle, lang, field, v string) string {
	tr := func(key string, args ...any) string { return b.T(lang, key, args...) }
	format := func(t time.Time) string { return t.Local().Format(tr("format.datetime")) }
	if field == "time" && v != "" { // "seconds|date|comment"
		parts := strings.SplitN(v, "|", 3)
		if secs, err := strconv.Atoi(parts[0]); err == nil && len(parts) == 3 {
			out := formatDuration(tr, secs)
			if d, err := time.Parse("2006-01-02", parts[1]); err == nil {
				out += " · " + d.Format(tr("format.date"))
			}
			if parts[2] != "" {
				out += " · " + parts[2]
			}
			return out
		}
	}
	key := map[string]string{"status": "status.", "priority": "priority.", "requires_control": "bool.",
		"allow_change_deadline": "bool.", "match_work_time": "bool.", "no_efficiency": "bool.",
		"dates_from_subtasks": "bool.", "auto_complete": "bool.", "result_required": "bool."}[field]
	if key != "" && b.Has(lang, key+v) {
		return tr(key + v)
	}
	if (field == "stage" || field == "sprint") && strings.Contains(v, ".") && b.Has(lang, v) {
		return tr(v)
	}
	if field == "estimate" && v != "" {
		if m, err := strconv.Atoi(v); err == nil {
			return formatDuration(tr, m*60)
		}
	}
	if field == "start_plan" || field == "end_plan" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			return t.Format(tr("format.date"))
		}
	}
	if field == "deadline" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return format(t)
		}
	}
	if field == "robot" { // "fail:kind:reason" — a robot that could not do its work
		if parts := strings.SplitN(v, ":", 3); len(parts) == 3 && parts[0] == "fail" {
			reason := parts[2]
			if b.Has(lang, reason) {
				reason = tr(reason)
			}
			return tr("taskrobot.fail", tr("taskrobot.kind."+parts[1]), reason)
		}
	}
	return v
}

// HistoryLine is one change of a task as a line of the task's log in the messenger (and anywhere outside the page).
func HistoryLine(b *i18n.Bundle, lang, actor, field, old, next string, at time.Time) string {
	when := at.Local().Format(b.T(lang, "format.datetime"))
	if field == "status" && next == "completed" {
		return b.T(lang, "matrix.status_done", when, actor)
	}
	line := "🕓 " + when + " — " + actor + ": " + b.T(lang, "field."+field)
	o, n := historyValue(b, lang, field, old), historyValue(b, lang, field, next)
	switch {
	case old != "" && next != "":
		line += ": " + o + " → " + n
	case next != "":
		line += ": " + n
	case old != "":
		line += ": " + b.T(lang, "history.removed") + " " + o
	}
	return line
}

// navMax limits the projects listed in the left menu; the rest are one click away in «Все проекты».
const navMax = 15

// navProjects picks the projects for the left menu: the user's own ones (in admin mode every project
// is visible, but the menu still lists only those the user belongs to). Collabs are under «Коллабы».
func navProjects(list []tasks.Project, admin bool) []tasks.Project {
	out := make([]tasks.Project, 0, navMax)
	for _, p := range list {
		if (admin && p.MyRole == "admin") || p.Collab() {
			continue
		}
		if len(out) == navMax {
			break
		}
		out = append(out, p)
	}
	return out
}

type taskList struct {
	Tasks []tasks.Task
	Cols  map[string]bool
	All   []string
	Gear  bool
}

type keptField struct{ Name, Value string }

// keepOf lists the task's current field values as hidden inputs, except the ones a small form edits itself:
// every task form posts to /t/{key}/edit, which saves all fields at once (with the version lock).
func keepOf(c tasks.TaskCard, except ...string) []keptField {
	skip := map[string]bool{}
	for _, e := range except {
		skip[e] = true
	}
	hm := func(m *int) string {
		if m == nil {
			return ""
		}
		return fmt.Sprintf("%d:%02d", *m/60, *m%60)
	}
	date := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.Format("2006-01-02")
	}
	deadline := ""
	if c.Deadline != nil {
		deadline = c.Deadline.Local().Format("2006-01-02T15:04")
	}
	all := []keptField{
		{"version", strconv.Itoa(c.Version)}, {"title", c.Title}, {"description", c.Description},
		{"responsible", c.ResponsibleID}, {"priority", strconv.Itoa(int(c.Priority))}, {"deadline", deadline},
		{"estimate", hm(c.EstimateMinutes)}, {"start_plan", date(c.StartPlan)}, {"end_plan", date(c.EndPlan)},
	}
	for name, on := range map[string]bool{"requires_control": c.RequiresControl,
		"allow_change_deadline": c.AllowChangeDeadline, "match_work_time": c.MatchWorkTime,
		"dates_from_subtasks": c.DatesFromSubtasks, "auto_complete": c.AutoComplete, "result_required": c.ResultRequired} {
		if on {
			all = append(all, keptField{name, "on"})
		}
	}
	out := all[:0]
	for _, f := range all {
		if !skip[f.Name] {
			out = append(out, f)
		}
	}
	return out
}

func formatDuration(tr func(string, ...any) string, secs int) string {
	h, m := secs/3600, secs%3600/60
	if h == 0 {
		return tr("time.m", m)
	}
	return tr("time.hm", h, m)
}

// buildPages parses every page together with the shared layout and the shared parts (templates/parts: the
// definitions several pages use, such as the reading pane's blocks of the mail), once per language.
func buildPages(b *i18n.Bundle) (map[string]map[string]*template.Template, error) {
	names, err := fs.Glob(templatesFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	parts, err := fs.Glob(templatesFS, "templates/parts/*.html")
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]*template.Template{}
	for _, l := range b.Langs() {
		out[l.Code] = map[string]*template.Template{}
		for _, n := range names {
			base := path.Base(n)
			if base == "layout.html" {
				continue
			}
			t, err := template.New("layout.html").Funcs(baseFuncs).Funcs(langFuncs(b, l.Code)).
				ParseFS(templatesFS, append([]string{"templates/layout.html", n}, parts...)...)
			if err != nil {
				return nil, err
			}
			out[l.Code][strings.TrimSuffix(base, ".html")] = t
		}
	}
	return out, nil
}

// guestIDs are the guests of collabs, for their mark beside their names (guestMark); every page refreshes it.
var guestIDs atomic.Pointer[map[string]bool]

// refreshGuests reads who the guests are.
func (s *Server) refreshGuests(r *http.Request) {
	rows, err := s.DB.Query(r.Context(), `SELECT id::text FROM users WHERE guest`)
	if err != nil {
		s.Log.Warn("guests", "err", err)
		return
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		s.Log.Warn("guests", "err", err)
		return
	}
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	guestIDs.Store(&m)
}

// page is the common data every page gets.
type page struct {
	Theme    bool // include /theme/theme.css
	Lang     string
	Langs    []i18n.Lang
	TitleKey string // locale key of the page title; Title is used when empty
	Title    string
	User     *auth.Session
	Guest    bool // the user is a guest from outside: the menu has their collabs' sections only
	Nav      string
	Flash    string
	Query    string // search box value
	Counters tasks.Counters
	Timer    *tasks.Timer    // the user's running timer, shown in the top bar
	Cols     map[string]bool // the user's task list columns (the gear in list headers)
	// left menu: the user's projects under «Проекты», and the project of the current page
	NavProjects    []tasks.Project
	NavProject     string
	FeedUnread     int              // important feed posts not confirmed yet
	ActingFor      []company.Person // people the user acts for today (menu «Задачи за …»)
	Unread         int              // unread notifications (the bell)
	MailUnread     int              // unread letters of the inboxes («Почта» in the menu)
	CalInvites     int              // meeting invitations not answered yet («Календарь» in the menu)
	Workday        *company.Workday // today's working day («Рабочее время» in the top bar); nil — not started
	WorkdayOn      bool             // the top bar shows the working day (signed-in pages)
	ReportDue      *company.ReportPeriod // a work report whose day has come is not sent yet
	Forgotten      *company.Workday // a past day left unfinished («Вы не завершили рабочий день»)
	HR             company.HR       // the user's role «Сотрудник кадров»
	CanInvite      bool             // «Пригласить сотрудника»: an administrator or an HR officer let invite
	VacationRemind bool             // no vacation in this year's plan («Укажите план отпуска»)
	Path           string           // this page (forms in the top bar come back to it)
	Presence       string           // the user's own status: "" (online), away, dnd — no pop-ups and sounds
	Portal         portal.Settings  // the portal's name and logo (the menu, the title)
	Menu           []menuEntry      // the left menu in the employee's order (not for guests)
	MenuHidden     []menuEntry      // its hidden items («Скрытые»)
	MenuOwn        bool             // the employee has set up a menu of their own
	Telephony      bool             // go24 has its Asterisk («Телефония» in the settings)
	Phone          string           // the user's inner number: «Позвонить» goes through the softphone ("" — none)
	PhoneURL       string           // the softphone window (/phone at go24's https address)
	Bare           bool             // a window of its own (the softphone): no menu, no top bar, no common scripts
	// messenger section of the menu (when Matrix is on)
	Messenger      string // Element URL
	MessengerSpace string
	MessengerRooms []matrixsync.RoomLink
	// «Коллабы» in the menu: tasks of the person's collabs needing attention and their chats, whose unread messages
	// menu.js adds (space-separated)
	CollabCounters tasks.CollabCounters
	CollabRooms    string
	Data           any
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, p page) {
	p.Lang = s.lang(r)
	p.Langs = s.I18n.Langs()
	p.Telephony = s.Telephony != nil
	if s.Portal != nil {
		p.Portal = s.Portal.Current()
	}
	t, ok := s.pages[p.Lang][name]
	if !ok {
		s.fail(w, "template", fmt.Errorf("no template %q for %s", name, p.Lang))
		return
	}
	if sess, err := s.session(r); err == nil {
		p.User = &sess
		if p.Guest = tasks.IsGuestCtx(r.Context()); p.Guest {
			sess.Admin, sess.AdminMode = false, false
		}
		if s.DB != nil {
			s.refreshGuests(r)
		}
		if s.Tasks != nil {
			if c, err := s.Tasks.Counters(r.Context(), sess.UserID); err == nil {
				p.Counters = c
			}
			if t, err := s.Tasks.RunningTimer(r.Context(), sess.UserID); err == nil {
				p.Timer = t
			}
			if c, err := s.Tasks.Columns(r.Context(), sess.UserID); err == nil {
				p.Cols = c
			}
			if s.Feed != nil && !p.Guest {
				p.FeedUnread, _ = s.Feed.Unread(r.Context(), sess.UserID)
			}
			p.Presence = s.myPresence(r.Context(), sess.UserID)
			if s.Telephony != nil && !p.Guest {
				p.Phone, _ = s.Telephony.NumberOf(r.Context(), sess.UserID)
				p.PhoneURL = s.phoneURL()
			}
			if s.Notify != nil {
				p.Unread, _ = s.Notify.Unread(r.Context(), sess.UserID)
			}
			if s.Mail != nil && !p.Guest {
				p.MailUnread, _ = s.Mail.Unread(r.Context(), sess.UserID)
			}
			if s.Calendar != nil && !p.Guest {
				p.CalInvites, _ = s.Calendar.Invitations(r.Context(), sess.UserID)
			}
			if s.Company != nil && !p.Guest { // a guest has no working day and acts for nobody
				p.ActingFor, _ = s.Company.ActingFor(r.Context(), sess.UserID, "view")
				p.Workday, _ = s.Company.Today(r.Context(), sess.UserID, time.Now())
				p.Forgotten, _ = s.Company.ForgottenDay(r.Context(), sess.UserID, time.Now())
				if sched, err := s.Tasks.Schedule(r.Context()); err == nil {
					p.ReportDue, _ = s.Company.ReportDue(r.Context(), sess.UserID, sched, time.Now())
				}
				p.WorkdayOn = true
				p.HR, _ = s.Company.HROf(r.Context(), sess.UserID)
				p.CanInvite = sess.Admin || p.HR.Can("invite")
				p.VacationRemind, _ = s.Company.VacationRemind(r.Context(), sess.UserID, time.Now())
			}
			if s.Matrix != nil {
				p.Messenger = s.Matrix.ElementURL
				p.MessengerSpace, p.MessengerRooms, _ = s.Matrix.Links(r.Context(), sess.UserID)
			}
			if c, ids, err := s.Tasks.CollabNav(r.Context(), sess.UserID); err == nil {
				p.CollabCounters = c
				if rooms, err := s.Matrix.ProjectRooms(r.Context(), ids); err == nil {
					p.CollabRooms = strings.Join(rooms, " ")
				}
			}
			if list, err := s.Tasks.ListProjects(r.Context(), sess.UserID); err == nil {
				p.NavProjects = navProjects(list, tasks.IsAdmin(r.Context()))
			}
			s.navMenu(r.Context(), &p)
		}
	}
	if rest, ok := strings.CutPrefix(r.URL.Path, "/p/"); ok {
		p.NavProject, _, _ = strings.Cut(rest, "/")
		p.NavProject = strings.ToUpper(p.NavProject)
	}
	p.Theme = s.hasThemeCSS()
	p.Path = r.URL.RequestURI()
	if p.Flash == "" {
		p.Flash = r.URL.Query().Get("msg")
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, p); err != nil {
		s.fail(w, "render "+name, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

const langCookie = "go24_lang"

// lang picks the UI language: explicit choice (cookie), then the portal's language, then the browser's
// Accept-Language, then the default.
func (s *Server) lang(r *http.Request) string {
	if c, err := r.Cookie(langCookie); err == nil && s.I18n.Supported(c.Value) {
		return c.Value
	}
	if s.Portal != nil {
		if l := s.Portal.Current().Lang; s.I18n.Supported(l) {
			return l
		}
	}
	return s.I18n.Match(r.Header.Get("Accept-Language"))
}

// setLang stores the chosen language and returns to the page the user came from.
func (s *Server) setLang(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if s.I18n.Supported(code) {
		http.SetCookie(w, &http.Cookie{Name: langCookie, Value: code, Path: "/", MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode})
	}
	back := "/"
	if ref, err := url.Parse(r.Referer()); err == nil && ref.Host == r.Host && ref.Path != "" {
		back = ref.RequestURI()
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// tr translates for the current request.
func (s *Server) tr(r *http.Request, key string, args ...any) string {
	return s.I18n.T(s.lang(r), key, args...)
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// formatSize is a file's size in the user's language: 820 Б, 1,5 КБ, 12 МБ.
func formatSize(tr func(string, ...any) string, n int64) string {
	units := []string{"size.b", "size.kb", "size.mb", "size.gb"}
	v, i := float64(n), 0
	for v >= 1024 && i < len(units)-1 {
		v, i = v/1024, i+1
	}
	num := strconv.FormatFloat(v, 'f', 0, 64)
	if i > 0 && v < 10 {
		num = strings.Replace(strconv.FormatFloat(v, 'f', 1, 64), ".", tr("size.point"), 1)
	}
	return num + " " + tr(units[i])
}

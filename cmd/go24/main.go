// Command go24 runs the go24 CRM web server.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"slices"
	"strings"
	"syscall"
	"time"

	"go24/internal/ai"
	"go24/internal/apikeys"
	"go24/internal/apps"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/authentik"
	"go24/internal/backup"
	"go24/internal/bi"
	"go24/internal/booking"
	"go24/internal/bp"
	"go24/internal/calendar"
	"go24/internal/calls"
	"go24/internal/company"
	"go24/internal/config"
	"go24/internal/crm"
	"go24/internal/db"
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
	"go24/internal/matrix"
	"go24/internal/matrixsync"
	"go24/internal/menu"
	"go24/internal/notify"
	"go24/internal/notifyout"
	"go24/internal/office"
	"go24/internal/openlines"
	"go24/internal/portal"
	"go24/internal/sign"
	"go24/internal/sites"
	"go24/internal/speech"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/sysinfo"
	"go24/internal/tasks"
	"go24/internal/telephony"
	"go24/internal/web"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	log.Info("database ready")
	// GO24_ROLE: web (pages, API, live updates), worker (messenger bridge, deliveries, schedules), all (both, default)
	role := os.Getenv("GO24_ROLE")
	if role == "" {
		role = "all"
	}
	if role != "web" && role != "worker" && role != "all" {
		return fmt.Errorf("GO24_ROLE must be web, worker or all, not %q", role)
	}
	serve, background := role != "worker", role != "web"
	log.Info("role", "role", role)

	store, err := storage.New(ctx, cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Bucket, cfg.S3UseSSL)
	if err != nil {
		return err
	}
	log.Info("storage ready", "endpoint", cfg.S3Endpoint, "bucket", cfg.S3Bucket)

	var rc *storage.RC
	if cfg.RcloneRCURL != "" {
		rc = storage.NewRC(cfg.RcloneRCURL, cfg.RcloneRCUser, cfg.RcloneRCPass)
		log.Info("user storages enabled", "rclone_rc", cfg.RcloneRCURL)
	}

	bundle, err := i18n.Load(cfg.LocalesDir)
	if err != nil {
		return err
	}
	log.Info("locales loaded", "languages", len(bundle.Langs()))
	// the portal's settings: its time zone (over GO24_TZ) is taken at start, the rest is read again every minute, so
	// the worker and other copies follow a change made on a page
	settings := portal.NewStore(pool, bundle)
	if set, err := settings.Get(ctx); err != nil {
		return err
	} else if set.TZ != "" {
		if loc, err := time.LoadLocation(set.TZ); err == nil {
			time.Local = loc
		}
	}
	go func() {
		for t := time.NewTicker(time.Minute); ; {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_, _ = settings.Get(ctx)
			}
		}
	}()

	stores := storages.NewStore(pool, rc, store)
	stores.Apps = map[string]storages.App{"gdrive": {ClientID: cfg.GDriveID, Secret: cfg.GDriveSecret},
		"dropbox": {ClientID: cfg.DropboxID, Secret: cfg.DropboxSecret}, "onedrive": {ClientID: cfg.OneDriveID, Secret: cfg.OneDriveSecret}}
	srv := &web.Server{
		DB:              pool,
		Storages:        stores,
		Tasks:           tasks.NewStore(pool),
		Company:         company.NewStore(pool),
		Authentik:       authentik.New(cfg.AuthentikURL, cfg.AuthentikToken),
		AuthentikPublic: publicOrigin(cfg.OIDCIssuer),
		Keys:            apikeys.NewStore(pool),
		Portal:          settings,
		Menus:           menu.NewStore(pool),
		Links:           linkpreview.New(),
		BaseURL:         cfg.BaseURL,
		ThemeDir:        cfg.ThemeDir,
		I18n:            bundle,
		AdminGroups:     cfg.AdminGroups,
		GuestGroups:     cfg.GuestGroups,
		Sessions:        auth.NewSessions(cfg.SessionSecret, strings.HasPrefix(cfg.BaseURL, "https://")),
		Log:             log,
	}
	srv.Feed = feed.NewStore(pool, srv.Tasks)
	srv.Notify = notify.NewStore(pool)
	srv.Calendar = calendar.NewStore(pool)
	srv.Calendar.Key = calendar.DavKey(cfg.SessionSecret)
	srv.KB = kb.NewStore(pool)
	srv.Sites = sites.NewStore(pool)
	srv.Booking = booking.NewStore(pool)
	srv.Sign = sign.NewStore(pool)
	srv.Drive = drive.NewStore(pool, srv.Tasks)
	srv.CRM = crm.NewStore(pool)
	srv.CRM.Tasks = srv.Tasks // robots make tasks
	srv.BI = bi.NewStore(pool, srv.CRM)
	if err := srv.BI.EnsureSystem(ctx); err != nil {
		return err
	}
	kick := make(chan struct{}, 1) // task robots run right after the change that queued them
	srv.Tasks.Kick = func() {
		select {
		case kick <- struct{}{}:
		default:
		}
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-kick:
				if _, err := srv.Tasks.RunRobotsDue(ctx, time.Now()); err != nil && ctx.Err() == nil {
					log.Error("task robots", "err", err)
				}
			}
		}
	}()
	srv.Marketing = marketing.NewStore(pool, srv.CRM)
	srv.BP = bp.NewStore(pool, srv.Tasks, srv.CRM, srv.Company)
	srv.BP.BaseURL = cfg.BaseURL
	srv.BP.Feed = srv.Feed           // standalone processes started by hand are posts
	srv.BP.Document = srv.BPDocument // «Создание документа» by the document generator
	// the processes of the documents of «Общий диск»
	srv.BP.Files = srv.Drive
	srv.BP.Calendar = srv.Calendar                                                                       // «Событие календаря»
	srv.BP.Tr = func(key string, args ...any) string { return bundle.T(bundle.Match(""), key, args...) } // «Да»/«Нет» of fields in texts
	// the robots of the CRM «Создать документ» and «Запустить бизнес-процесс»
	srv.CRM.MakeDocument = func(ctx context.Context, userID, entity, entityID, tplID string) (string, error) {
		return srv.BPDocument(ctx, userID, entity, entityID, tplID, nil)
	}
	srv.CRM.StartBP = srv.BP.StartRobot
	srv.Hooks = hooks.NewStore(pool)
	srv.Apps = apps.NewStore(pool)
	srv.Audit = audit.NewStore(pool)
	srv.IPAccess = ipaccess.NewStore(pool)
	if cfg.BackupDir != "" { // «Резервное копирование»: the pages ask for copies, go24-worker makes them
		srv.Backup, srv.BackupHostDir = backup.NewStore(pool, cfg.SessionSecret), cfg.BackupHostDir
		if rc != nil { // the outside places go through rclone-rcd, which sees the backups' folder at the same path
			srv.BackupRC = rc
		}
		if background {
			go (&backup.Runner{Store: srv.Backup, Root: cfg.BackupDir, ConfigDir: cfg.BackupConfigDir, RC: srv.BackupRC, RCRoot: cfg.BackupDir,
				DatabaseURL: cfg.DatabaseURL, AdminGroups: srv.AdminGroups, Objects: store, MatrixDir: cfg.BackupMatrixDir, Log: log,
				Migrate: func(ctx context.Context) error { return db.Migrate(ctx, pool) }}).Loop(ctx)
		}
	}
	srv.OpenLines = openlines.NewStore(pool, srv.CRM)
	srv.OpenLines.Tr = func(key string, args ...any) string { return bundle.T(bundle.Match(""), key, args...) } // service lines of dialogues
	// the lines' Telegram bots: their tokens sealed, the files in the built-in storage
	srv.OpenLines.Key = []byte(cfg.SessionSecret)
	srv.OpenLines.PutFile = func(ctx context.Context, key string, r io.Reader, size int64, ct string) error {
		return srv.Storages.Put(ctx, "", false, "", key, r, size, ct)
	}
	srv.OpenLines.OpenFile = func(ctx context.Context, key string) (io.ReadCloser, error) { return srv.Storages.Open(ctx, nil, key) }
	srv.Mail = mail.NewStore(pool, cfg.SessionSecret)
	mailApp := func(id, secret, fallbackID, fallbackSecret string) mail.OAuthApp {
		if id == "" {
			return mail.OAuthApp{ClientID: fallbackID, Secret: fallbackSecret}
		}
		return mail.OAuthApp{ClientID: id, Secret: secret}
	}
	srv.Mail.OAuth = map[string]mail.OAuthApp{
		"google":    mailApp(cfg.MailGoogleID, cfg.MailGoogleSecret, cfg.GDriveID, cfg.GDriveSecret),
		"microsoft": mailApp(cfg.MailMicrosoftID, cfg.MailMicrosoftSecret, cfg.OneDriveID, cfg.OneDriveSecret),
		"yandex":    {ClientID: cfg.MailYandexID, Secret: cfg.MailYandexSecret}}
	srv.AI = ai.NewStore(pool, cfg.SessionSecret)
	srv.CRM.SendMail = func(ctx context.Context, userID, to, subject, text string) error { // mail robots: the first mailbox
		boxes, err := srv.Mail.Mailboxes(ctx, userID)
		if err != nil {
			return err
		}
		if len(boxes) == 0 {
			return i18n.E("err.robot_mailbox")
		}
		return srv.Mail.Send(ctx, userID, boxes[0].ID, to, subject, text)
	}
	srv.CRM.BookingURL = func(ctx context.Context, linkID string) (string, error) { // booking robots: a link that is on
		l, err := srv.Booking.Link(ctx, linkID)
		if err == nil && !l.Active {
			err = i18n.E("err.robot_booking")
		}
		return strings.TrimRight(cfg.BaseURL, "/") + "/bk/" + l.Token, err
	}
	srv.OpenLines.SendMail = srv.CRM.SendMail                                                    // the conversation a visitor asked for: from the operator's mailbox
	srv.Mail.Keep = func(ctx context.Context, owner, letterID string, f mail.Attachment) error { // in the built-in storage
		key := path.Join("go24", "mail", letterID, randomKey(), path.Base(strings.ReplaceAll(f.Name, "\\", "/")))
		if err := srv.Storages.Put(ctx, owner, false, "", key, bytes.NewReader(f.Data), int64(len(f.Data)), f.ContentType); err != nil {
			return err
		}
		_, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: key, Name: f.Name, ContentType: f.ContentType, Size: int64(len(f.Data)),
			UploadedBy: owner, MailID: letterID, ContentID: f.ContentID})
		return err
	}
	srv.Mail.Open = srv.Storages.Open // a draft's files are read back when it is sent
	srv.Mail.Log = func(what, id string, err error) { log.Warn(what, "id", id, "err", err) }
	srv.Mail.Tr = func(key string, args ...any) string { return bundle.T(bundle.Match(""), key, args...) } // automatic replies, forwards
	// «Создавать лид для новых адресов» of a mailbox
	srv.Mail.Lead = func(ctx context.Context, owner string, l mail.NewLead) (string, error) {
		return srv.CRM.SaveLead(ctx, owner, crm.Lead{Title: l.Title, Name: l.Name, Email: l.Email, Comment: l.Comment,
			Source: srv.Mail.Tr("mail.source")})
	}
	srv.Calls, srv.CallURL = calls.New(cfg.LiveKitURL, cfg.LiveKitKey, cfg.LiveKitSecret), cfg.CallURL
	srv.External = calls.NewExternal(pool, cfg.SessionSecret) // Zoom, Телемост, VK, Telegram, Max — with or without LiveKit
	if srv.Calls != nil {
		srv.Calls.APIURL = cfg.LiveKitAPIURL
		srv.Rings, srv.CallLines, srv.CallGuests = &calls.Rings{DB: pool}, &calls.Sessions{DB: pool}, &calls.Guests{DB: pool}
		srv.Recordings, srv.RecordDir = &calls.Recordings{DB: pool}, cfg.RecordDir
		if background && cfg.LiveKitAPIURL != "" && cfg.RecordDir != "" {
			go srv.RecordingsLoop(ctx) // a finished recording goes to the Drive and the call's chat
		}
	}
	if cfg.AsteriskURL != "" && cfg.TelephonyDir != "" {
		srv.Telephony = &telephony.PBX{Store: telephony.NewStore(pool, cfg.SessionSecret), Dir: cfg.TelephonyDir, SIPHost: cfg.SIPHost,
			Records: cfg.PhoneRecords, Key: cfg.AsteriskSecret, ARI: &telephony.ARI{URL: cfg.AsteriskURL, User: "go24", Password: cfg.AsteriskSecret}}
		if serve { // the accounts' configuration is in a volume of the web container
			go srv.Telephony.Loop(ctx, func(err error) { log.Warn("telephony", "err", err) })
		}
		srv.CRM.InfoCall = srv.Telephony.InfoCall // infocall robots
	}
	// notifications in outside messengers: the company's bots, the employees' chats with them (GO24DEV-560)
	srv.NotifyOut = notifyout.New(pool, cfg.SessionSecret)
	srv.NotifyOut.Notify, srv.NotifyOut.AppURL, srv.NotifyOut.Log = srv.Notify, cfg.BaseURL, log
	srv.NotifyOut.Tr = func(key string, args ...any) string { return bundle.T(bundle.Match(""), key, args...) }
	srv.NotifyOut.Text = func(it notify.Item) string {
		tr := srv.NotifyOut.Tr
		return notify.Text(it, tr, func(t time.Time) string { return t.Local().Format(tr("format.datetime")) })
	}
	if background {
		go srv.NotifyOut.Run(ctx)
	}
	srv.Speech = speech.New(cfg.WhisperURL)
	if background && srv.Recordings != nil && srv.Speech != nil {
		go srv.TranscriptsLoop(ctx) // the text of saved recordings of calls (GO24DEV-798)
	}
	srv.Office = office.New(cfg.OfficeURL, cfg.OfficeSecret, cfg.OfficeSelf)
	// «Состояние системы»: the services checked, the folders shown, and this copy saying it is alive
	srv.SysProbes = append([]sysinfo.Probe{{ID: "storage", Target: cfg.S3Endpoint, Check: store.Ping}},
		slices.Concat(sysinfo.HTTPProbe("authentik", cfg.AuthentikURL, "/-/health/live/"),
			sysinfo.HTTPProbe("matrix", cfg.MatrixURL, "/_matrix/client/versions"),
			sysinfo.HTTPProbe("livekit", cfg.LiveKitAPIURL, "/"),
			sysinfo.HTTPProbe("office", cfg.OfficeURL, "/healthcheck"),
			sysinfo.HTTPProbe("asterisk", cfg.AsteriskURL, "/ari/asterisk/ping"),
			sysinfo.HTTPProbe("whisper", cfg.WhisperURL, "/"))...)
	if rc != nil {
		srv.SysProbes = append(srv.SysProbes, sysinfo.Probe{ID: "rclone", Target: cfg.RcloneRCURL, Check: rc.Ping})
	}
	for _, d := range []web.SysDisk{{ID: "backup", Path: cfg.BackupDir}, {ID: "records", Path: cfg.RecordDir}, {ID: "telephony", Path: cfg.TelephonyDir}} {
		if d.Path != "" {
			srv.SysDisks = append(srv.SysDisks, d)
		}
	}
	go sysinfo.NewStore(pool).Beat(ctx, role, func(err error) { log.Warn("system heartbeat", "err", err) })
	if serve {
		go srv.RefreshMentionNames(ctx)
	}
	if background {
		startBackground(ctx, srv, log)
	}
	// Live task chat: PostgreSQL NOTIFY → open task cards
	srv.Live = live.NewBroker()
	if serve {
		go srv.Live.Listen(ctx, pool, "go24_comments", log)
		go srv.Live.Listen(ctx, pool, "go24_feed", log)
		go srv.Live.Listen(ctx, pool, "go24_notify", log)
		go srv.Live.Listen(ctx, pool, "go24_openlines", log)
	}

	// Messenger: go24 keeps its Matrix server in step (accounts, space, project rooms, task threads)
	if mc := matrix.New(cfg.MatrixURL, cfg.MatrixServerName, cfg.MatrixToken); mc != nil {
		company := "go24"
		_ = pool.QueryRow(ctx, `SELECT name FROM departments WHERE parent_id IS NULL`).Scan(&company)
		srv.Matrix = matrixsync.New(&matrixsync.Service{DB: pool, M: mc, Tasks: srv.Tasks, Feed: srv.Feed, Log: log, IDP: "oidc-authentik",
			ElementURL: cfg.MatrixElementURL, AppURL: cfg.BaseURL, Company: company,
			ThreadHint: bundle.T(bundle.Match(""), "matrix.thread_hint"), FeedName: bundle.T(bundle.Match(""), "matrix.feed_room"), GuestMark: bundle.T(bundle.Match(""), "collab.guest"),
			Notify: srv.Notify, NotifyRoom: bundle.T(bundle.Match(""), "matrix.notify_room"),
			HistoryText: func(actor, acting, field, old, next string, at time.Time) string {
				lang := bundle.Match("")
				if acting != "" {
					actor += " (" + bundle.T(lang, "acting.sign", acting) + ")"
				}
				return web.HistoryLine(bundle, lang, actor, field, old, next, at)
			},
			CollabLogText: func(e tasks.CollabLogEntry) string { return web.CollabLogLine(bundle, bundle.Match(""), e) },
			NotifyText: func(it notify.Item) string {
				lang := bundle.Match("")
				tr := func(key string, args ...any) string { return bundle.T(lang, key, args...) }
				return notify.Text(it, tr, func(t time.Time) string { return t.Local().Format(tr("format.datetime")) })
			}})
		srv.Tasks.Comment = func(ctx context.Context, userID, taskID, body string) error { // robots' comments go as people's do
			_, err := srv.Matrix.PostTaskComment(ctx, userID, taskID, body)
			return err
		}
		srv.BP.Chat = func(ctx context.Context, fromID, toID, text string) error { // «Сообщение в чат» of processes: a direct chat
			room, err := srv.Matrix.DirectChat(ctx, fromID, toID)
			if err != nil {
				return err
			}
			_, err = srv.Matrix.Send(ctx, fromID, room, text, "")
			return err
		}
		srv.Matrix.BotQueued = srv.Apps.Wake
		if background {
			go srv.Matrix.Run(ctx)
			go srv.Matrix.RemindLoop(ctx, func(m matrixsync.Message) string { return matrixsync.Excerpt(m, 140) }) // «Напомнить» of messages
			if srv.CallLines != nil && cfg.LiveKitAPIURL != "" {
				go srv.CallLinesLoop(ctx) // a call of a chat that is over: its line says so
			}
		} else {
			srv.Matrix.Remote = true
		}
		log.Info("matrix messenger enabled", "server", cfg.MatrixServerName)
	}
	if cfg.OIDCEnabled() {
		srv.OIDC = auth.NewOIDC(cfg.OIDCIssuer, cfg.OIDCClientID, cfg.OIDCClientSecret, cfg.BaseURL+"/auth/callback")
	} else {
		log.Warn("GO24_OIDC_* not set: login disabled until Authentik is configured")
	}

	if !serve { // go24 worker: no pages, only the background until it is stopped
		log.Info("worker running")
		<-ctx.Done()
		return nil
	}
	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	log.Info("listening", "addr", cfg.Addr, "base_url", cfg.BaseURL)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// publicOrigin is scheme://host of a URL ("" when there is none): the OIDC issuer gives Authentik's public address.
func publicOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// randomKey is a random part of a storage key.
func randomKey() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

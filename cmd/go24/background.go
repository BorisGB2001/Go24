package main

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"go24/internal/web"
)

// startBackground runs the scheduled work of go24 worker: meeting reminders, regular tasks, cleaning old
// notifications. (The messenger bridge, its deliveries and the task log start with the Matrix service.)
func startBackground(ctx context.Context, srv *web.Server, log *slog.Logger) {
	// open lines: the Telegram, MAX and Viber bots', VK communities', OK groups', Avito accounts', WhatsApp numbers' and applications' channels' messages and the answers to them
	go srv.OpenLines.RunTelegram(ctx, log)
	go srv.OpenLines.RunMax(ctx, log)
	go srv.OpenLines.RunVK(ctx, log)
	go srv.OpenLines.RunViber(ctx, log)
	go srv.OpenLines.RunOK(ctx, log)
	go srv.OpenLines.RunAvito(ctx, log)
	go srv.OpenLines.RunWhatsApp(ctx, log)
	go srv.OpenLines.RunConnectors(ctx, log) // the applications' channels (imconnector)
	go func() { // open lines: CoPilot's summaries of the dialogues just closed — apart, the AI answers slowly
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			srv.OLSummarizeDue(ctx, time.Now())
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	go func() { // open lines: the AI bot on the knowledge base answers the clients waiting for it
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for {
			srv.OLAIBotDue(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	go func() { // «Платёжные системы»: the online payments the services have not told about
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			srv.PayCheckDue(ctx)
		}
	}()
	go func() { // meeting, task and CRM activity reminders; CRM waits over; CRM robots that waited, mail triggers, recurring deals; task robots; open lines
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			if _, err := srv.Calendar.RemindDue(ctx, time.Now()); err != nil && ctx.Err() == nil {
				log.Error("calendar reminders", "err", err)
			}
			if _, err := srv.Tasks.RemindDue(ctx, time.Now()); err != nil && ctx.Err() == nil {
				log.Error("task reminders", "err", err)
			}
			if _, err := srv.Tasks.TimeOverDue(ctx, time.Now()); err != nil && ctx.Err() == nil { // «Плановые трудозатраты» passed
				log.Error("task time over", "err", err)
			}
			if _, err := srv.CRM.RemindDue(ctx, time.Now()); err != nil && ctx.Err() == nil {
				log.Error("crm activity reminders", "err", err)
			}
			if _, err := srv.CRM.EndWaitsDue(ctx, time.Now()); err != nil && ctx.Err() == nil { // «Ожидание» whose day has come
				log.Error("crm waits", "err", err)
			}
			if _, err := srv.CRM.RunRobotsDue(ctx, time.Now()); err != nil && ctx.Err() == nil {
				log.Error("crm robots", "err", err)
			}
			if _, err := srv.CRM.RunTriggersDue(ctx, time.Now()); err != nil && ctx.Err() == nil {
				log.Error("crm triggers", "err", err)
			}
			made, errs := srv.CRM.RunRecurringDue(ctx, time.Now()) // «Регулярные сделки»: the copies whose day has come
			for _, err := range errs {
				log.Error("crm recurring deal", "err", err)
			}
			if len(made) > 0 {
				log.Info("crm recurring deals made", "deals", len(made))
			}
			if err := srv.CRM.PurgeRecycleDue(ctx, time.Now()); err != nil && ctx.Err() == nil { // «Корзина»: 30 days
				log.Error("crm recycle bin", "err", err)
			}
			if _, err := srv.Tasks.RunRobotsDue(ctx, time.Now()); err != nil && ctx.Err() == nil {
				log.Error("task robots", "err", err)
			}
			if _, err := srv.OpenLines.Due(ctx, time.Now()); err != nil && ctx.Err() == nil { // open lines: the queue, automatic messages, closing
				log.Error("open lines", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	go func() { // calendars connected by their iCal links: loaded again every half an hour
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			if err := srv.SyncCalendarLinks(ctx, time.Now()); err != nil && ctx.Err() == nil {
				log.Error("calendar links", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	go func() { // calendars of CalDAV servers connected two ways: every five minutes, within a minute after a change
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			if err := srv.SyncCalendarDav(ctx, time.Now()); err != nil && ctx.Err() == nil {
				log.Error("calendar caldav", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	go func() { // «Маркетинг»: the campaigns' letters, a hundred a minute; the cards of «Генератор продаж», two hundred
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			if n, err := srv.Marketing.SendDue(ctx, time.Now(), srv.MarketingSend); err != nil && ctx.Err() == nil {
				log.Error("marketing campaigns", "err", err)
			} else if n > 0 {
				log.Info("marketing letters sent", "letters", n)
			}
			if n, err := srv.Marketing.MakeSalesGenCards(ctx, ""); err != nil && ctx.Err() == nil { // «Генератор продаж»
				log.Error("sales generator", "err", err)
			} else if n > 0 {
				log.Info("sales generator cards made", "cards", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	go func() { // regular tasks: templates make their tasks on schedule (and for new employees)
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			made, errs := srv.Tasks.RunDue(ctx, time.Now())
			for _, k := range made {
				log.Info("regular task made", "task", k)
			}
			for _, err := range errs {
				log.Error("regular task", "err", err)
			}
			// «Шаблон для новых сотрудников»: the tasks of employees who came since
			made, errs = srv.Tasks.RunNewUsers(ctx, time.Now())
			for _, k := range made {
				log.Info("task for a new employee made", "task", k)
			}
			for _, err := range errs {
				log.Error("task for a new employee", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	go func() { // task and drive trash: what is kept longer than the company setting goes for good, hourly; CRM rates and scoring; learning reminders
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			if n, err := srv.Tasks.PurgeExpired(ctx, time.Now()); err != nil && ctx.Err() == nil {
				log.Error("task trash", "err", err)
			} else if n > 0 {
				log.Info("task trash purged", "tasks", n)
			}
			// «Валюты» of the CRM: the rates of the Central Bank, once a day
			if n, err := srv.CRM.FetchRates(ctx, false); err != nil && ctx.Err() == nil {
				log.Warn("crm currency rates", "err", err)
			} else if n > 0 {
				log.Info("crm currency rates", "currencies", n)
			}
			if _, err := srv.CRM.TrainScoringDue(ctx, time.Now()); err != nil && ctx.Err() == nil { // «Скоринг сделок»: learned daily
				log.Error("crm scoring", "err", err)
			}
			if n, err := srv.Company.RemindLearning(ctx, time.Now()); err != nil && ctx.Err() == nil { // «Обучение»: due days
				log.Error("learning reminders", "err", err)
			} else if n > 0 {
				log.Info("learning reminders", "sent", n)
			}
			if n, err := srv.Drive.PurgeExpired(ctx, time.Now()); err != nil && ctx.Err() == nil {
				log.Error("drive trash", "err", err)
			} else if n > 0 {
				log.Info("drive trash purged", "items", n)
			}
			if days, err := srv.Tasks.TrashDays(ctx); err == nil && days > 0 {
				if n, err := srv.KB.PurgeExpired(ctx, time.Now().AddDate(0, 0, -days)); err != nil && ctx.Err() == nil {
					log.Error("kb trash", "err", err)
				} else if n > 0 {
					log.Info("kb trash purged", "items", n)
				}
				if n, err := srv.Sites.PurgeExpired(ctx, time.Now().AddDate(0, 0, -days)); err != nil && ctx.Err() == nil {
					log.Error("sites trash", "err", err)
				} else if n > 0 {
					log.Info("sites trash purged", "items", n)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	go func() { // outgoing webhooks (n8n …) and events of applications: every 15 s
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			for _, err := range errsOf(srv.Hooks.Deliver(ctx, time.Now())) {
				log.Warn("webhook", "err", err)
			}
			for _, err := range errsOf(srv.Apps.DeliverEvents(ctx, time.Now(), srv.BaseURL)) {
				log.Warn("app event", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-srv.Apps.Woken(): // a message to a chat bot: its application hears it at once
			}
		}
	}()
	go func() { // «Бизнес-процессы»: pauses that are over, autostart on CRM cards and documents of «Общий диск» added or changed — every 15 s
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			for _, err := range slices.Concat(srv.BP.WakeDue(ctx, time.Now()), srv.BP.AutoStart(ctx), srv.BP.AutoStartFiles(ctx), srv.BP.AutoStartLists(ctx)) {
				log.Error("business process", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	go func() { // «Почта»: letters whose time has come are sent («Отправить позже», and after the undo delay)
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			for _, err := range srv.Mail.SendDue(ctx) {
				log.Warn("mail send", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	go func() { // «Почта»: «Экспорт» of letters to a file, one after another
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			for _, err := range srv.RunMailExports(ctx) {
				log.Warn("mail export", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	go func() { // «Почта»: «Импорт из файла», one after another
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			for _, err := range srv.RunMailImports(ctx) {
				log.Warn("mail import", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	// «Почта»: the inboxes over IMAP IDLE — new letters and those read on the phone at once
	go srv.Mail.Watch(ctx)
	go func() { // «Почта»: new letters of the connected mailboxes (with the rules and automatic replies); snoozed letters
		// come back; the flags' reminders; the standing «Очистка»; hourly the retention policies («Назначить политику»)
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		var retained time.Time
		for {
			if err := srv.Mail.WakeDue(ctx); err != nil {
				log.Warn("mail snooze", "err", err)
			}
			if err := srv.Mail.RemindDue(ctx); err != nil {
				log.Warn("mail remind", "err", err)
			}
			for _, err := range srv.Mail.SyncDue(ctx, 2*time.Minute) {
				log.Warn("mail sync", "err", err)
			}
			for _, err := range srv.Mail.SweepDue(ctx) {
				log.Warn("mail sweep", "err", err)
			}
			if time.Since(retained) >= time.Hour {
				retained = time.Now()
				n, errs := srv.Mail.RetentionDue(ctx)
				for _, err := range errs {
					log.Warn("mail retention", "err", err)
				}
				if n > 0 {
					log.Info("mail retention", "letters", n)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	go func() { // «Диск»: the text of new files and versions is read for the search, a batch at a time
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			for ctx.Err() == nil {
				n, err := srv.IndexDrive(ctx, 50)
				if err != nil && ctx.Err() == nil {
					log.Error("drive content", "err", err)
				}
				if err != nil || n < 50 {
					break
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	go func() { // «Онлайн-запись»: the letters to the clients, the reminders, the lateness
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			if err := srv.BookingDue(ctx); err != nil && ctx.Err() == nil {
				log.Warn("booking due", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	go func() { // «Подпись»: the signers whose turn has lasted are reminded
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			if err := srv.SignDue(ctx, time.Now()); err != nil && ctx.Err() == nil {
				log.Warn("sign reminders", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	go func() { // «Журнал событий»: the events older than the period of keeping go
		for t := time.NewTicker(6 * time.Hour); ; <-t.C {
			if n, err := srv.Audit.Prune(ctx, time.Now()); err != nil && ctx.Err() == nil {
				log.Error("audit prune", "err", err)
			} else if n > 0 {
				log.Info("audit pruned", "events", n)
			}
			if ctx.Err() != nil {
				return
			}
		}
	}()
	go func() { // read notifications are kept for 90 days
		for t := time.NewTicker(6 * time.Hour); ; <-t.C {
			if err := srv.Notify.Prune(ctx, 90*24*time.Hour); err != nil && ctx.Err() == nil {
				log.Error("notify prune", "err", err)
			}
			if ctx.Err() != nil {
				return
			}
		}
	}()
	go func() { // «Почта»: letters deleted for good are kept for «Восстановить удалённые элементы» RecoverDays
		for t := time.NewTicker(6 * time.Hour); ; <-t.C {
			if err := srv.Mail.ExpireRecoverable(ctx); err != nil && ctx.Err() == nil {
				log.Error("mail recoverable expire", "err", err)
			}
			if ctx.Err() != nil {
				return
			}
		}
	}()

}

// errsOf drops the count a delivery round gives along with its errors.
func errsOf(_ int, errs []error) []error { return errs }

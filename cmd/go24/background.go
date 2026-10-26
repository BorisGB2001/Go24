package main

import (
	"context"
	"log/slog"
	"time"

	"go24/internal/web"
)

// startBackground runs the scheduled work of go24 worker: meeting reminders, regular tasks, cleaning old
// notifications. (The messenger bridge, its deliveries and the task log start with the Matrix service.)
func startBackground(ctx context.Context, srv *web.Server, log *slog.Logger) {
	go func() { // meeting, task and CRM activity reminders; CRM robots that waited, mail triggers, recurring deals; task robots; open lines
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			if _, err := srv.Calendar.RemindDue(ctx, time.Now()); err != nil && ctx.Err() == nil {
				log.Error("calendar reminders", "err", err)
			}
			if _, err := srv.Tasks.RemindDue(ctx, time.Now()); err != nil && ctx.Err() == nil {
				log.Error("task reminders", "err", err)
			}
			if _, err := srv.CRM.RemindDue(ctx, time.Now()); err != nil && ctx.Err() == nil {
				log.Error("crm activity reminders", "err", err)
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
	go func() { // «Маркетинг»: the campaigns' letters, a hundred a minute
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			if n, err := srv.Marketing.SendDue(ctx, time.Now(), srv.MarketingSend); err != nil && ctx.Err() == nil {
				log.Error("marketing campaigns", "err", err)
			} else if n > 0 {
				log.Info("marketing letters sent", "letters", n)
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
	go func() { // task and drive trash: what is kept longer than the company setting goes for good, hourly; CRM rates
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
			}
		}
	}()
	go func() { // «Бизнес-процессы»: pauses that are over, autostart on CRM cards added or changed — every 15 s
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			for _, err := range append(srv.BP.WakeDue(ctx, time.Now()), srv.BP.AutoStart(ctx)...) {
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
	// «Почта»: the inboxes over IMAP IDLE — new letters and those read on the phone at once
	go srv.Mail.Watch(ctx)
	go func() { // «Почта»: new letters of the connected mailboxes (with the rules and automatic replies); snoozed letters
		// come back; the flags' reminders; the standing «Очистка»
		t := time.NewTicker(time.Minute)
		defer t.Stop()
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

}

// errsOf drops the count a delivery round gives along with its errors.
func errsOf(_ int, errs []error) []error { return errs }

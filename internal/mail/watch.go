package mail

import (
	"context"
	"time"

	"github.com/emersion/go-imap/client"
)

// Mail at once, as Outlook has it: the inbox of each mailbox is watched over a connection of its own (IMAP IDLE) —
// a letter that comes is fetched, sorted and announced at once, and one read, moved or deleted on the phone changes
// the counters at once; the sync every few minutes (SyncDue) still brings the other folders. A server without IDLE
// is asked every minute (NOOP); and as not every server tells an idling session of flags changed in another one
// (GreenMail does not), the inbox's flags are looked at every watchRecheck too.

// watchPause: a watch that broke (the server away, a wrong password) is started again after it.
const watchPause = 5 * time.Minute

// watchRecheck: how often the flags of a watched inbox are looked at (a variable for the tests).
var watchRecheck = time.Minute

// watchDebounce: the updates of a moment (a letter and its flags, many read at once) are one fetch.
const watchDebounce = 2 * time.Second

// Watch keeps the mailboxes' inboxes watched till ctx ends (the worker runs it): a mailbox connected later is watched
// within a minute, one removed is let go.
func (s *Store) Watch(ctx context.Context) {
	type watch struct {
		stop  context.CancelFunc
		done  chan struct{}
		retry time.Time // written before done is closed
	}
	running := map[string]*watch{}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		rows, err := s.db.Query(ctx, `SELECT id::text FROM mailboxes ORDER BY created_at`)
		var ids []string
		if err == nil {
			ids, err = pgxCollectStrings(rows)
		}
		if err != nil && ctx.Err() == nil && s.Log != nil {
			s.Log("mail: watch", "", err)
		}
		want := map[string]bool{}
		for _, id := range ids {
			want[id] = true
			if w := running[id]; w != nil {
				select {
				case <-w.done:
					if time.Now().Before(w.retry) {
						continue
					}
				default:
					continue
				}
			}
			wctx, cancel := context.WithCancel(ctx)
			w := &watch{stop: cancel, done: make(chan struct{})}
			running[id] = w
			go func(id string) {
				defer close(w.done)
				err := s.watchInbox(wctx, id)
				w.retry = time.Now().Add(watchPause)
				if err != nil && wctx.Err() == nil && s.Log != nil {
					s.Log("mail: watch", id, err)
				}
			}(id)
		}
		for id, w := range running {
			if !want[id] && err == nil {
				w.stop()
				delete(running, id)
			}
		}
		select {
		case <-ctx.Done():
			for _, w := range running {
				w.stop()
			}
			return
		case <-t.C:
		}
	}
}

// watchInbox fetches the mailbox's inbox each time the server tells of a change, till ctx ends or the connection
// breaks.
func (s *Store) watchInbox(ctx context.Context, id string) error {
	a, err := s.account(ctx, id)
	if err != nil {
		return err
	}
	f, err := s.folderByRole(ctx, id, "inbox")
	if err != nil || f.ID == "" || f.Local { // not synced yet: the next try
		return err
	}
	if err := s.authorize(ctx, &a); err != nil {
		return err
	}
	c, err := dialIMAP(a.IMAPHost, a.IMAPPort, a.TLS)
	if err != nil {
		return err
	}
	ictx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer c.Logout()
	if err := imapLogin(c, a); err != nil {
		return err
	}
	updates, poke := make(chan client.Update, 64), make(chan struct{}, 1)
	c.Updates = updates
	go func() {
		for {
			select {
			case <-updates:
				select {
				case poke <- struct{}{}:
				default:
				}
			case <-ictx.Done():
				return
			}
		}
	}()
	for {
		news, changed, err := s.fetchFolder(ctx, c, a, f)
		if err != nil {
			return err
		}
		s.landed(ctx, a.UserID, id, news, changed)
		select { // what the fetch itself stirred up is no news
		case <-poke:
		default:
		}
		stop, done := make(chan struct{}), make(chan error, 1)
		go func() {
			done <- c.Idle(stop, &client.IdleOptions{LogoutTimeout: 20 * time.Minute, PollInterval: time.Minute})
		}()
		select {
		case <-ctx.Done():
			close(stop)
			<-done
			return nil
		case err := <-done: // the connection broke
			return err
		case <-time.After(watchRecheck):
		case <-poke:
			select {
			case <-ctx.Done():
			case <-time.After(watchDebounce):
			}
		}
		close(stop)
		if err := <-done; err != nil || ctx.Err() != nil {
			return err
		}
	}
}

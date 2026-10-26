// Package live delivers database notifications to open pages: a Broker listens to a PostgreSQL channel
// and fans the payloads out to subscribers by key (here: task ID → new comment IDs).
package live

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Broker struct {
	mu   sync.Mutex
	subs map[string]map[chan string]struct{}
}

func NewBroker() *Broker { return &Broker{subs: map[string]map[chan string]struct{}{}} }

// Subscribe returns a channel of payloads for key and a function that ends the subscription.
func (b *Broker) Subscribe(key string) (<-chan string, func()) {
	ch := make(chan string, 16)
	b.mu.Lock()
	if b.subs[key] == nil {
		b.subs[key] = map[chan string]struct{}{}
	}
	b.subs[key][ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs[key], ch)
		if len(b.subs[key]) == 0 {
			delete(b.subs, key)
		}
		b.mu.Unlock()
	}
}

// Publish hands value to every subscriber of key; a slow subscriber misses it rather than blocking others.
func (b *Broker) Publish(key, value string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[key] {
		select {
		case ch <- value:
		default:
		}
	}
}

// Listen forwards NOTIFY payloads "key:value" of a channel to the broker until ctx ends, reconnecting on errors.
func (b *Broker) Listen(ctx context.Context, pool *pgxpool.Pool, channel string, log *slog.Logger) {
	for ctx.Err() == nil {
		err := func() error {
			conn, err := pool.Acquire(ctx)
			if err != nil {
				return err
			}
			defer conn.Release()
			if _, err := conn.Exec(ctx, "LISTEN "+channel); err != nil {
				return err
			}
			for {
				n, err := conn.Conn().WaitForNotification(ctx)
				if err != nil {
					return err
				}
				if key, value, ok := strings.Cut(n.Payload, ":"); ok {
					b.Publish(key, value)
				}
			}
		}()
		if ctx.Err() == nil {
			log.Error("live: listen", "channel", channel, "err", err)
			time.Sleep(3 * time.Second)
		}
	}
}

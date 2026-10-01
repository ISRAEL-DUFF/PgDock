package jobs

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Channel is the Postgres NOTIFY channel the operations table trigger
// publishes to on inserts, log appends, and status changes.
const Channel = "pgdock_operations"

type event struct {
	ID     uuid.UUID `json:"id"`
	Status string    `json:"status"`
}

// Notifier holds one LISTEN connection and fans operation change events out
// to workers (to claim new work immediately) and SSE streams (to push log
// lines). Signals are coalesced: a subscriber learns *that* something
// changed and re-reads the row, so a dropped duplicate loses nothing.
//
// After a reconnect every subscriber is signalled, because notifications
// sent while disconnected are lost.
type Notifier struct {
	pool *pgxpool.Pool
	log  *slog.Logger

	mu     sync.Mutex
	subs   map[uuid.UUID]map[chan struct{}]struct{}
	queued map[chan struct{}]struct{}
}

// NewNotifier returns a Notifier; call Run to start listening.
func NewNotifier(pool *pgxpool.Pool, log *slog.Logger) *Notifier {
	return &Notifier{
		pool:   pool,
		log:    log,
		subs:   map[uuid.UUID]map[chan struct{}]struct{}{},
		queued: map[chan struct{}]struct{}{},
	}
}

// Subscribe returns a channel signalled whenever operation id changes, and a
// function to stop the subscription.
func (n *Notifier) Subscribe(id uuid.UUID) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	n.mu.Lock()
	if n.subs[id] == nil {
		n.subs[id] = map[chan struct{}]struct{}{}
	}
	n.subs[id][ch] = struct{}{}
	n.mu.Unlock()
	return ch, func() {
		n.mu.Lock()
		delete(n.subs[id], ch)
		if len(n.subs[id]) == 0 {
			delete(n.subs, id)
		}
		n.mu.Unlock()
	}
}

// SubscribeQueued returns a channel signalled whenever an operation becomes
// queued (new, retried, or reclaimed).
func (n *Notifier) SubscribeQueued() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	n.mu.Lock()
	n.queued[ch] = struct{}{}
	n.mu.Unlock()
	return ch, func() {
		n.mu.Lock()
		delete(n.queued, ch)
		n.mu.Unlock()
	}
}

// Run listens until ctx is done, reconnecting with backoff on errors.
func (n *Notifier) Run(ctx context.Context) {
	backoff := 250 * time.Millisecond
	for {
		err := n.listen(ctx)
		if ctx.Err() != nil {
			return
		}
		n.log.Warn("operation notifier disconnected; reconnecting", "err", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 10*time.Second)
	}
}

func (n *Notifier) listen(ctx context.Context) error {
	pooled, err := n.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	// LISTEN state is per connection; take this one out of the pool for good.
	conn := pooled.Hijack()
	defer conn.Close(context.Background())

	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{Channel}.Sanitize()); err != nil {
		return err
	}
	n.broadcast() // catch up on anything missed while disconnected
	for {
		msg, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		var ev event
		if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
			n.log.Warn("bad operation notification", "payload", msg.Payload, "err", err)
			continue
		}
		n.dispatch(ev)
	}
}

func (n *Notifier) dispatch(ev event) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for ch := range n.subs[ev.ID] {
		signal(ch)
	}
	if ev.Status == StatusQueued {
		for ch := range n.queued {
			signal(ch)
		}
	}
}

func (n *Notifier) broadcast() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, set := range n.subs {
		for ch := range set {
			signal(ch)
		}
	}
	for ch := range n.queued {
		signal(ch)
	}
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

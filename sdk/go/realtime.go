package pgdock

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Change is a database change realtime delivered.
type Change struct {
	Type            string          `json:"type"` // INSERT, UPDATE, DELETE
	Schema          string          `json:"schema"`
	Table           string          `json:"table"`
	CommitTimestamp string          `json:"commit_timestamp"`
	Record          json.RawMessage `json:"record"`
	OldRecord       json.RawMessage `json:"old_record"`
}

// ChangeFilter picks a table's changes.
type ChangeFilter struct {
	Event  string `json:"event"` // "*" (default), INSERT, UPDATE, DELETE
	Schema string `json:"schema"`
	Table  string `json:"table"`
	Filter string `json:"filter,omitempty"` // column=eq.value, …
}

// Realtime is one WebSocket for any number of channels. It connects on the
// first Subscribe and reconnects (and rejoins) until Close.
type Realtime struct {
	c *Client

	mu       sync.Mutex
	conn     *websocket.Conn
	channels map[string]*Channel
	ref      int
	cancel   context.CancelFunc
	running  bool
	waiters  map[string]chan reply
}

type reply struct {
	Status   string          `json:"status"`
	Response json.RawMessage `json:"response"`
}

type wireMsg struct {
	Topic   string          `json:"topic"`
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload"`
	Ref     *string         `json:"ref"`
	JoinRef *string         `json:"join_ref,omitempty"`
}

func newRealtime(c *Client) *Realtime {
	return &Realtime{c: c, channels: map[string]*Channel{}, waiters: map[string]chan reply{}}
}

// Channel is a topic's database changes, broadcasts and presence.
type Channel struct {
	rt      *Realtime
	Name    string
	Private bool
	// BroadcastSelf gets this client's own broadcasts back.
	BroadcastSelf bool

	mu       sync.Mutex
	changes  []changeSub
	bcast    []bcSub
	resyncs  []func()
	statuses []func(status string, err error)
	joined   bool
	joinRef  string
}

type changeSub struct {
	f  ChangeFilter
	cb func(Change)
	id int
}

type bcSub struct {
	event string
	cb    func(event string, payload json.RawMessage)
}

// Channel is a channel on the connection; add callbacks, then Subscribe.
func (r *Realtime) Channel(name string) *Channel {
	return &Channel{rt: r, Name: name}
}

func (ch *Channel) topic() string { return "realtime:" + ch.Name }

// OnChange is called with each change to the table that the caller may see.
func (ch *Channel) OnChange(f ChangeFilter, cb func(Change)) *Channel {
	if f.Event == "" {
		f.Event = "*"
	}
	if f.Schema == "" {
		f.Schema = "public"
	}
	ch.changes = append(ch.changes, changeSub{f: f, cb: cb})
	return ch
}

// OnBroadcast is called with broadcasts of event ("*" for all).
func (ch *Channel) OnBroadcast(event string, cb func(event string, payload json.RawMessage)) *Channel {
	ch.bcast = append(ch.bcast, bcSub{event: event, cb: cb})
	return ch
}

// OnResync is called when changes were dropped (the server's rate cap, a
// lost database connection) or the connection came back: refetch.
func (ch *Channel) OnResync(cb func()) *Channel {
	ch.resyncs = append(ch.resyncs, cb)
	return ch
}

// OnStatus is called with SUBSCRIBED, CHANNEL_ERROR and CLOSED.
func (ch *Channel) OnStatus(cb func(status string, err error)) *Channel {
	ch.statuses = append(ch.statuses, cb)
	return ch
}

func (ch *Channel) status(s string, err error) {
	for _, cb := range ch.statuses {
		cb(s, err)
	}
}

// Subscribe joins the channel, connecting first if needed, and waits for
// the join's answer.
func (ch *Channel) Subscribe(ctx context.Context) error {
	r := ch.rt
	r.mu.Lock()
	r.channels[ch.topic()] = ch
	r.mu.Unlock()
	if err := r.ensure(ctx); err != nil {
		return err
	}
	return ch.join(ctx)
}

// Unsubscribe leaves the channel.
func (ch *Channel) Unsubscribe(ctx context.Context) error {
	r := ch.rt
	r.mu.Lock()
	delete(r.channels, ch.topic())
	r.mu.Unlock()
	_, err := r.push(ctx, ch.topic(), "phx_leave", map[string]any{}, ch.joinRef, true)
	return err
}

// Send broadcasts event with payload to the channel's other members.
func (ch *Channel) Send(ctx context.Context, event string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = ch.rt.push(ctx, ch.topic(), "broadcast", map[string]any{"type": "broadcast", "event": event, "payload": json.RawMessage(b)}, ch.joinRef, false)
	return err
}

// Track shares state on the channel's presence.
func (ch *Channel) Track(ctx context.Context, state map[string]any) error {
	_, err := ch.rt.push(ctx, ch.topic(), "presence", map[string]any{"type": "presence", "event": "track", "payload": state}, ch.joinRef, false)
	return err
}

func (ch *Channel) join(ctx context.Context) error {
	r := ch.rt
	tok, err := r.c.accessToken(ctx)
	if err != nil {
		return err
	}
	pcs := make([]ChangeFilter, len(ch.changes))
	for i, s := range ch.changes {
		pcs[i] = s.f
	}
	cfg := map[string]any{
		"config": map[string]any{
			"broadcast":        map[string]any{"self": ch.BroadcastSelf, "ack": false},
			"presence":         map[string]any{"key": ""},
			"postgres_changes": pcs,
			"private":          ch.Private,
		},
	}
	if tok != "" {
		cfg["access_token"] = tok
	}
	r.mu.Lock()
	r.ref++
	ref := strconv.Itoa(r.ref)
	r.mu.Unlock()
	ch.joinRef = ref
	rep, err := r.push(ctx, ch.topic(), "phx_join", cfg, ref, true)
	if err != nil {
		ch.status("CHANNEL_ERROR", err)
		return err
	}
	if rep.Status != "ok" {
		var why struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(rep.Response, &why)
		err := &Error{Code: "join_refused", Message: why.Reason}
		ch.status("CHANNEL_ERROR", err)
		return err
	}
	var resp struct {
		PostgresChanges []struct {
			ID int `json:"id"`
		} `json:"postgres_changes"`
	}
	_ = json.Unmarshal(rep.Response, &resp)
	ch.mu.Lock()
	for i, b := range resp.PostgresChanges {
		if i < len(ch.changes) {
			ch.changes[i].id = b.ID
		}
	}
	ch.joined = true
	ch.mu.Unlock()
	ch.status("SUBSCRIBED", nil)
	return nil
}

func (ch *Channel) handle(m wireMsg) {
	switch m.Event {
	case "postgres_changes":
		var p struct {
			IDs  []int  `json:"ids"`
			Data Change `json:"data"`
		}
		if json.Unmarshal(m.Payload, &p) != nil {
			return
		}
		ch.mu.Lock()
		subs := append([]changeSub(nil), ch.changes...)
		ch.mu.Unlock()
		for _, s := range subs {
			for _, id := range p.IDs {
				if id == s.id {
					s.cb(p.Data)
					break
				}
			}
		}
	case "broadcast":
		var p struct {
			Event   string          `json:"event"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(m.Payload, &p) != nil {
			return
		}
		for _, s := range ch.bcast {
			if s.event == "*" || s.event == p.Event {
				s.cb(p.Event, p.Payload)
			}
		}
	case "system":
		var p struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(m.Payload, &p)
		if p.Message == "resync" {
			ch.resync()
		} else if p.Status == "error" {
			ch.status("CHANNEL_ERROR", errors.New(p.Message))
		}
	case "phx_close":
		ch.mu.Lock()
		ch.joined = false
		ch.mu.Unlock()
		ch.status("CLOSED", nil)
	}
}

func (ch *Channel) resync() {
	for _, cb := range ch.resyncs {
		cb()
	}
}

// ensure connects once and starts the reader, which reconnects.
func (r *Realtime) ensure(ctx context.Context) error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return nil
	}
	r.running = true
	bg, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.mu.Unlock()
	conn, err := r.dial(ctx)
	if err != nil {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
		cancel()
		return err
	}
	r.mu.Lock()
	r.conn = conn
	r.mu.Unlock()
	go r.loop(bg, conn)
	return nil
}

func (r *Realtime) dial(ctx context.Context) (*websocket.Conn, error) {
	u, _ := url.Parse(r.c.URL + "/realtime/v1/websocket")
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.RawQuery = url.Values{"apikey": {r.c.key}, "vsn": {"1.0.0"}}.Encode()
	conn, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: r.c.hc, HTTPHeader: r.c.hdr.Clone()})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(1 << 20)
	return conn, nil
}

// loop reads messages, heartbeats, and on a lost connection redials
// and rejoins every channel, telling each to resync.
func (r *Realtime) loop(ctx context.Context, conn *websocket.Conn) {
	delays := []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second}
	for attempt := 0; ; {
		hb, stop := context.WithCancel(ctx)
		go func() {
			t := time.NewTicker(25 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-hb.Done():
					return
				case <-t.C:
					_, _ = r.push(hb, "phoenix", "heartbeat", map[string]any{}, "", false)
				}
			}
		}()
		for {
			var m wireMsg
			if err := wsjson.Read(ctx, conn, &m); err != nil {
				break
			}
			if m.Event == "phx_reply" && m.Ref != nil {
				var rep reply
				_ = json.Unmarshal(m.Payload, &rep)
				r.mu.Lock()
				w := r.waiters[*m.Ref]
				delete(r.waiters, *m.Ref)
				r.mu.Unlock()
				if w != nil {
					w <- rep
				}
				continue
			}
			r.mu.Lock()
			ch := r.channels[m.Topic]
			r.mu.Unlock()
			if ch != nil {
				ch.handle(m)
			}
		}
		stop()
		r.mu.Lock()
		r.conn = nil
		for ref, w := range r.waiters {
			close(w)
			delete(r.waiters, ref)
		}
		chans := make([]*Channel, 0, len(r.channels))
		for _, ch := range r.channels {
			chans = append(chans, ch)
		}
		r.mu.Unlock()
		for {
			if ctx.Err() != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delays[min(attempt, len(delays)-1)]):
			}
			attempt++
			dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			c, err := r.dial(dctx)
			cancel()
			if err == nil {
				conn = c
				break
			}
		}
		attempt = 0
		r.mu.Lock()
		r.conn = conn
		r.mu.Unlock()
		go func() {
			for _, ch := range chans {
				jctx, cancel := context.WithTimeout(ctx, 15*time.Second)
				if ch.join(jctx) == nil {
					ch.resync()
				}
				cancel()
			}
		}()
	}
}

// push sends an event; with wait, it returns the reply.
func (r *Realtime) push(ctx context.Context, topic, event string, payload any, joinRef string, wait bool) (reply, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return reply{}, err
	}
	r.mu.Lock()
	conn := r.conn
	ref := joinRef
	if event != "phx_join" {
		r.ref++
		ref = strconv.Itoa(r.ref)
	}
	var w chan reply
	if wait {
		w = make(chan reply, 1)
		r.waiters[ref] = w
	}
	r.mu.Unlock()
	if conn == nil {
		return reply{}, &Error{Code: "not_connected", Message: "realtime isn't connected"}
	}
	m := wireMsg{Topic: topic, Event: event, Payload: b, Ref: &ref}
	if joinRef != "" {
		m.JoinRef = &joinRef
	}
	if err := wsjson.Write(ctx, conn, m); err != nil {
		return reply{}, err
	}
	if !wait {
		return reply{}, nil
	}
	select {
	case rep, ok := <-w:
		if !ok {
			return reply{}, &Error{Code: "connection_lost", Message: "the connection closed before the reply"}
		}
		return rep, nil
	case <-ctx.Done():
		r.mu.Lock()
		delete(r.waiters, ref)
		r.mu.Unlock()
		return reply{}, ctx.Err()
	}
}

// SetToken tells joined channels about a new access token.
func (r *Realtime) SetToken(ctx context.Context, token string) {
	r.mu.Lock()
	chans := make([]*Channel, 0, len(r.channels))
	for _, ch := range r.channels {
		chans = append(chans, ch)
	}
	r.mu.Unlock()
	for _, ch := range chans {
		_, _ = r.push(ctx, ch.topic(), "access_token", map[string]string{"access_token": token}, ch.joinRef, false)
	}
}

// Close ends the connection and every channel.
func (r *Realtime) Close() {
	r.mu.Lock()
	cancel, conn := r.cancel, r.conn
	r.running, r.conn, r.channels = false, nil, map[string]*Channel{}
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}
}

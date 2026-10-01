package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/store"
)

// Streamer serves an operation's progress as Server-Sent Events.
//
// Events:
//
//	event: log     id: <1-based index>  data: LogEntry
//	event: status  data: {"status","attempts","error"}
//	event: done    data: {"status","error"}   (then the stream closes)
//
// Clients that reconnect with Last-Event-ID resume after that log entry.
type Streamer struct {
	db       store.DBTX
	notifier *Notifier
	log      *slog.Logger
	// done ends every open stream (server shutdown).
	done <-chan struct{}
	// Poll is a safety-net re-read interval in case a notification is
	// missed; it also paces keepalive comments. Default 5s.
	Poll time.Duration
}

// NewStreamer returns a Streamer. Open streams end when ctx is done, so
// long-lived SSE connections don't hold up graceful shutdown.
func NewStreamer(ctx context.Context, db store.DBTX, notifier *Notifier, log *slog.Logger) *Streamer {
	return &Streamer{db: db, notifier: notifier, log: log, done: ctx.Done(), Poll: 5 * time.Second}
}

type statusEvent struct {
	Status   string  `json:"status"`
	Attempts int32   `json:"attempts"`
	Error    *string `json:"error"`
}

// ServeHTTP-style entry point for one operation's stream.
func (s *Streamer) Serve(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	ctx := r.Context()
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Subscribe before the first read so no change slips between them.
	changed, unsub := s.notifier.Subscribe(id)
	defer unsub()

	op, err := Get(ctx, s.db, id)
	if errors.Is(err, ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "not_found", "operation not found")
		return
	}
	if err != nil {
		s.log.Error("stream operation", "operation_id", id, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal", "could not load operation")
		return
	}

	sent := 0
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			sent = n
		}
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // disable proxy buffering
	w.WriteHeader(http.StatusOK)

	lastStatus := ""
	ticker := time.NewTicker(s.Poll)
	defer ticker.Stop()
	for {
		entries, err := DecodeLog(op.Log)
		if err != nil {
			s.log.Error("stream operation", "operation_id", id, "err", err)
			return
		}
		for ; sent < len(entries); sent++ {
			if err := writeEvent(w, "log", strconv.Itoa(sent+1), entries[sent]); err != nil {
				return
			}
		}
		if op.Status != lastStatus {
			lastStatus = op.Status
			if err := writeEvent(w, "status", "", statusEvent{op.Status, op.Attempts, op.Error}); err != nil {
				return
			}
		}
		if IsTerminal(op.Status) {
			_ = writeEvent(w, "done", "", statusEvent{op.Status, op.Attempts, op.Error})
			flusher.Flush()
			return
		}
		flusher.Flush()

		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case <-changed:
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
		}

		op, err = Get(ctx, s.db, id)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Error("stream operation", "operation_id", id, "err", err)
			}
			return
		}
	}
}

func writeEvent(w http.ResponseWriter, event, id string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if id != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", id); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	return err
}

func writeJSONError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": msg})
}

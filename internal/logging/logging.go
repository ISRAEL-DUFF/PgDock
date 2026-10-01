// Package logging builds the process-wide slog logger.
package logging

import (
	"io"
	"log/slog"
)

// New returns a logger writing to w in the given format ("json" or "text").
func New(w io.Writer, format string, level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	if format == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

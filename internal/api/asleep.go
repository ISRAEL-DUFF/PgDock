package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/freetier"
)

// Retry-After for a project waking up: a resume takes seconds, a restore
// from the archive about a minute.
const (
	resumeRetryAfter  = 10
	restoreRetryAfter = 60
)

// writeAsleep answers a request that needs the database of a Free project
// paused or archived for inactivity (V3 §4.2): it queues the resume, or
// the restore, and answers 503 with Retry-After, as the edge and the
// waker do. It reports whether the project was asleep.
func (s *Server) writeAsleep(ctx context.Context, w http.ResponseWriter, projectID uuid.UUID, lifecycle string) bool {
	code, msg, retry := "", "", 0
	switch lifecycle {
	case freetier.Paused:
		code, msg, retry = "project_resuming", "the project was paused for inactivity and is resuming; retry shortly", resumeRetryAfter
	case freetier.Archived:
		code, msg, retry = "project_restoring", "the project was archived for inactivity and is being restored; retry in a minute or two", restoreRetryAfter
	default:
		return false
	}
	if s.freetier != nil {
		// Already waking, or another operation running: it wakes after.
		if _, err := s.freetier.Resume(context.WithoutCancel(ctx), projectID, nil); err != nil && !errors.Is(err, freetier.ErrConflict) {
			s.log.Warn("wake a project for an API request", "project_id", projectID, "err", err)
		}
	}
	w.Header().Set("Retry-After", strconv.Itoa(retry))
	writeError(w, http.StatusServiceUnavailable, code, msg)
	return true
}

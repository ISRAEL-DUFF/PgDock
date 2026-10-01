package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/store"
)

// ListOperations implements GET /api/v1/operations.
func (s *Server) ListOperations(w http.ResponseWriter, r *http.Request, params gen.ListOperationsParams) {
	if !s.requireDB(w) {
		return
	}
	limit := 50
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > 200 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be between 1 and 200")
			return
		}
		limit = *params.Limit
	}
	arg := store.ListOperationsParams{Kind: params.Kind, ProjectID: params.ProjectId, MaxRows: int32(limit)}
	if params.Status != nil {
		if !params.Status.Valid() {
			writeError(w, http.StatusBadRequest, "bad_request", "unknown status")
			return
		}
		st := string(*params.Status)
		arg.Status = &st
	}

	ops, err := store.New(s.db).ListOperations(r.Context(), arg)
	if err != nil {
		s.internalError(w, "list operations", err)
		return
	}
	out := gen.OperationList{Items: make([]gen.Operation, 0, len(ops))}
	for _, op := range ops {
		o, err := toAPIOperation(op)
		if err != nil {
			s.internalError(w, "list operations", err)
			return
		}
		out.Items = append(out.Items, o)
	}
	writeJSON(w, http.StatusOK, out)
}

// GetOperation implements GET /api/v1/operations/{id}.
func (s *Server) GetOperation(w http.ResponseWriter, r *http.Request, id gen.OperationID) {
	if !s.requireDB(w) {
		return
	}
	op, err := jobs.Get(r.Context(), s.db, id)
	if errors.Is(err, jobs.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "operation not found")
		return
	}
	if err != nil {
		s.internalError(w, "get operation", err)
		return
	}
	o, err := toAPIOperation(op)
	if err != nil {
		s.internalError(w, "get operation", err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// StreamOperation implements GET /api/v1/operations/{id}/stream (SSE).
func (s *Server) StreamOperation(w http.ResponseWriter, r *http.Request, id gen.OperationID) {
	if s.streamer == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "operation streaming is not configured")
		return
	}
	s.streamer.Serve(w, r, id)
}

// CreateDevOperation implements POST /api/v1/dev/operations.
func (s *Server) CreateDevOperation(w http.ResponseWriter, r *http.Request) {
	if !s.dev {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
		return
	}
	if !s.requireDB(w) {
		return
	}
	var p gen.NoopParams
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&p); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return
	}
	params := jobs.NoopParams{Steps: 3, DelayMS: 500}
	if p.Steps != nil {
		params.Steps = *p.Steps
	}
	if p.DelayMs != nil {
		params.DelayMS = *p.DelayMs
	}
	if p.FailAttempts != nil {
		params.FailAttempts = *p.FailAttempts
	}
	if params.Steps < 1 || params.Steps > 100 || params.DelayMS < 0 || params.DelayMS > 10000 || params.FailAttempts < 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "steps must be 1-100, delay_ms 0-10000, fail_attempts >= 0")
		return
	}

	op, err := jobs.Enqueue(r.Context(), s.db, jobs.EnqueueParams{Kind: jobs.KindNoop, Params: params})
	if err != nil {
		s.internalError(w, "enqueue operation", err)
		return
	}
	o, err := toAPIOperation(op)
	if err != nil {
		s.internalError(w, "enqueue operation", err)
		return
	}
	w.Header().Set("Location", "/api/v1/operations/"+op.ID.String())
	writeJSON(w, http.StatusAccepted, o)
}

func (s *Server) requireDB(w http.ResponseWriter) bool {
	if s.db == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "metadata database is not configured")
		return false
	}
	return true
}

func (s *Server) internalError(w http.ResponseWriter, what string, err error) {
	s.log.Error(what, "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}

func toAPIOperation(op store.Operation) (gen.Operation, error) {
	entries, err := jobs.DecodeLog(op.Log)
	if err != nil {
		return gen.Operation{}, err
	}
	logs := make([]gen.OperationLogEntry, len(entries))
	for i, e := range entries {
		logs[i] = gen.OperationLogEntry{
			Ts:    e.TS,
			Step:  e.Step,
			Level: gen.OperationLogEntryLevel(e.Level),
			Msg:   e.Msg,
		}
	}
	params := map[string]any{}
	if len(op.Params) > 0 {
		if err := json.Unmarshal(op.Params, &params); err != nil {
			return gen.Operation{}, err
		}
	}
	// Encrypted handoff data never leaves the server.
	delete(params, "secrets")
	return gen.Operation{
		Id:         op.ID,
		Kind:       op.Kind,
		ProjectId:  op.ProjectID,
		Params:     params,
		Status:     gen.OperationStatus(op.Status),
		Attempts:   int(op.Attempts),
		RunAfter:   op.RunAfter,
		Log:        logs,
		Error:      op.Error,
		CreatedBy:  op.CreatedBy,
		CreatedAt:  op.CreatedAt,
		FinishedAt: op.FinishedAt,
	}, nil
}

// Package pgdock is PGDock's Go client: the data API, auth, storage and
// realtime of a project with backend services (V4 §8).
//
//	c, err := pgdock.New("https://k7f3m2q9.api.pgdock.ng", os.Getenv("PGDOCK_SECRET_KEY"))
//	var todos []Todo
//	_, err = c.Data.From("todos").Select("id,title").Eq("done", false).Limit(20).Into(ctx, &todos)
package pgdock

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// Error is every failure the API reports: its status, code and request id.
// A failure to reach the API is returned as is (a *url.Error).
type Error struct {
	Status     int
	Code       string
	Message    string
	RequestID  string
	Details    json.RawMessage
	RetryAfter int
}

func (e *Error) Error() string {
	if e.RequestID != "" {
		return fmt.Sprintf("pgdock: %d %s: %s (request %s)", e.Status, e.Code, e.Message, e.RequestID)
	}
	return fmt.Sprintf("pgdock: %d %s: %s", e.Status, e.Code, e.Message)
}

// IsCode reports whether err is an API error with this code.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// apiError reads {"error":{"code","message","request_id","details"}}.
func apiError(res *http.Response) error {
	e := &Error{Status: res.StatusCode, Code: "http_" + strconv.Itoa(res.StatusCode), Message: res.Status, RequestID: res.Header.Get("X-Request-Id")}
	if ra, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil {
		e.RetryAfter = ra
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var body struct {
		Error *struct {
			Code      string          `json:"code"`
			Message   string          `json:"message"`
			RequestID string          `json:"request_id"`
			Details   json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &body) == nil && body.Error != nil {
		if body.Error.Code != "" {
			e.Code = body.Error.Code
		}
		if body.Error.Message != "" {
			e.Message = body.Error.Message
		}
		if body.Error.RequestID != "" {
			e.RequestID = body.Error.RequestID
		}
		e.Details = body.Error.Details
	}
	return e
}

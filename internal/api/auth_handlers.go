package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/auth"
)

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// authError maps auth errors to responses; safe messages pass through.
func (s *Server) authError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, auth.ErrRateLimited):
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate_limited", err.Error())
	case errors.Is(err, auth.ErrLocked):
		writeError(w, http.StatusTooManyRequests, "locked", err.Error())
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid_credentials", err.Error())
	case errors.Is(err, auth.ErrChallengeExpired):
		writeError(w, http.StatusUnauthorized, "challenge_expired", err.Error())
	case errors.Is(err, auth.ErrBadSetupCode):
		writeError(w, http.StatusForbidden, "bad_setup_code", err.Error())
	case errors.Is(err, auth.ErrSetupDone):
		writeError(w, http.StatusConflict, "setup_done", err.Error())
	case errors.Is(err, auth.ErrWeakPassword), errors.Is(err, auth.ErrInvalidEmail):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	default:
		s.internalError(w, what, err)
	}
}

func (s *Server) sessionState(w http.ResponseWriter, r *http.Request, sess *auth.Session) gen.SessionState {
	out := gen.SessionState{CsrfToken: s.ensureCSRF(w, r)}
	idle := int(s.auth.IdleTimeout().Seconds())
	out.IdleTimeoutSeconds = &idle
	if sess != nil {
		out.Authenticated = true
		out.Operator = &gen.Operator{Id: sess.OperatorID, Email: openapi_types.Email(sess.Email), Role: gen.OperatorRole(sess.Role)}
		if sess.ReauthAt != nil {
			until := sess.ReauthAt.Add(s.auth.ReauthWindow())
			if until.After(time.Now()) {
				out.ReauthUntil = &until
			}
		}
	}
	return out
}

// GetSession implements GET /api/v1/session.
func (s *Server) GetSession(w http.ResponseWriter, r *http.Request) {
	needed, err := s.auth.SetupNeeded(r.Context())
	if err != nil {
		s.internalError(w, "session", err)
		return
	}
	var sp *auth.Session
	if sess, ok := sessionFrom(r.Context()); ok {
		sp = &sess
	}
	st := s.sessionState(w, r, sp)
	st.SetupRequired = needed
	writeJSON(w, http.StatusOK, st)
}

// BeginSetup implements POST /api/v1/setup/begin.
func (s *Server) BeginSetup(w http.ResponseWriter, r *http.Request) {
	var req gen.SetupBeginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a := auditFrom(r.Context())
	a.set("email", string(req.Email))
	if err := s.auth.Allow(ipString(r.Context())); err != nil {
		s.authError(w, "setup", err)
		return
	}
	enr, err := s.auth.BeginSetup(r.Context(), req.SetupCode, string(req.Email), req.Password)
	if err != nil {
		s.authError(w, "setup", err)
		return
	}
	writeJSON(w, http.StatusOK, gen.SetupEnrollment{
		EnrollmentToken: enr.Token, TotpSecret: enr.Secret, TotpUri: enr.URI, ExpiresAt: enr.Expires,
	})
}

// CompleteSetup implements POST /api/v1/setup/complete.
func (s *Server) CompleteSetup(w http.ResponseWriter, r *http.Request) {
	var req gen.SetupCompleteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.auth.Allow(ipString(r.Context())); err != nil {
		s.authError(w, "setup", err)
		return
	}
	token, sess, err := s.auth.CompleteSetup(r.Context(), req.EnrollmentToken, req.Code, ipFrom(r.Context()), r.UserAgent())
	if err != nil {
		s.authError(w, "setup", err)
		return
	}
	a := auditFrom(r.Context())
	a.operatorID = &sess.OperatorID
	a.target("operator", sess.OperatorID.String())
	s.log.Info("owner account created", "email", sess.Email)
	s.setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, s.sessionState(w, r, &sess))
}

// PostAuthLogin implements POST /api/v1/auth/login.
func (s *Server) PostAuthLogin(w http.ResponseWriter, r *http.Request) {
	var req gen.LoginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a := auditFrom(r.Context())
	a.set("email", string(req.Email))
	if err := s.auth.Allow(ipString(r.Context())); err != nil {
		s.authError(w, "login", err)
		return
	}
	challenge, opID, err := s.auth.Login(r.Context(), string(req.Email), req.Password)
	if opID.String() != "00000000-0000-0000-0000-000000000000" {
		a.operatorID = &opID
	}
	if err != nil {
		s.authError(w, "login", err)
		return
	}
	writeJSON(w, http.StatusOK, gen.LoginChallenge{ChallengeId: challenge})
}

// PostAuthTotp implements POST /api/v1/auth/totp.
func (s *Server) PostAuthTotp(w http.ResponseWriter, r *http.Request) {
	var req gen.TotpRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.auth.Allow(ipString(r.Context())); err != nil {
		s.authError(w, "totp", err)
		return
	}
	token, sess, err := s.auth.CompleteLogin(r.Context(), req.ChallengeId, req.Code, ipFrom(r.Context()), r.UserAgent())
	if err != nil {
		s.authError(w, "totp", err)
		return
	}
	auditFrom(r.Context()).operatorID = &sess.OperatorID
	s.setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, s.sessionState(w, r, &sess))
}

// PostAuthReauth implements POST /api/v1/auth/reauth.
func (s *Server) PostAuthReauth(w http.ResponseWriter, r *http.Request) {
	var req gen.ReauthRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	sess, _ := sessionFrom(r.Context())
	if err := s.auth.Allow(ipString(r.Context())); err != nil {
		s.authError(w, "reauth", err)
		return
	}
	if err := s.auth.Reauthenticate(r.Context(), sess, req.Password, req.Code); err != nil {
		s.authError(w, "reauth", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PostAuthLogout implements POST /api/v1/auth/logout.
func (s *Server) PostAuthLogout(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r.Context())
	if err := s.auth.Logout(r.Context(), sess); err != nil {
		s.internalError(w, "logout", err)
		return
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// GetMe implements GET /api/v1/me.
func (s *Server) GetMe(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r.Context())
	writeJSON(w, http.StatusOK, gen.Operator{Id: sess.OperatorID, Email: openapi_types.Email(sess.Email), Role: gen.OperatorRole(sess.Role)})
}

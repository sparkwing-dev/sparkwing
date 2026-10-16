package controller

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type sessionProofKey struct{}

type sessionProof struct {
	session *store.Session
	role    store.Role
}

func (s *Server) sessionEndpoints() []Endpoint {
	return []Endpoint{
		{Method: "GET", Pattern: "/api/v1/auth/session", Authentication: capability.AuthBrowserFlow, Verify: s.verifySessionFlow, Handle: s.handleSession},
		{Method: "POST", Pattern: "/api/v1/auth/logout", Authentication: capability.AuthBrowserFlow, Verify: s.verifySessionFlow, Handle: s.handleLogout},
	}
}

func (s *Server) sessionPresentation(r *http.Request) (string, error) {
	if values := r.Header.Values("Authorization"); len(values) != 0 {
		if len(values) == 1 {
			fields := strings.Fields(values[0])
			if len(fields) == 2 && strings.EqualFold(fields[0], "Session") {
				return fields[1], nil
			}
		}
		return "", sessionUnavailable()
	}
	var raw string
	for _, cookie := range r.Cookies() {
		if cookie.Name == cookieName(sessionCookieName, s.cookiesSecure()) {
			if raw != "" || cookie.Value == "" {
				return "", sessionUnavailable()
			}
			raw = cookie.Value
		}
	}
	if raw == "" {
		return "", sessionUnavailable()
	}
	return raw, nil
}

func (s *Server) verifySessionFlow(r *http.Request) error {
	session, err := s.readSessionFlow(r)
	if err != nil {
		return err
	}
	var role store.Role
	if unsafeMethod(r.Method) {
		if !validAPIRequestCSRF(r, session.CSRFToken, s.cookiesSecure()) {
			return apierr.Authorization(apierr.ReasonReauthRequired, "invalid CSRF token")
		}
	} else {
		raw, err := s.sessionPresentation(r)
		if err != nil {
			return err
		}
		session, err = s.store.LookupSessionAndRenew(r.Context(), raw, sessionTTL, s.sessionMaxLifetime)
		if err != nil {
			return sessionFlowError(err)
		}
		if err := s.validateSessionFlow(session); err != nil {
			return err
		}
		role, err = s.memberRole(r.Context(), session.Team, session.AccountID)
		if err != nil {
			return sessionFlowError(err)
		}
	}
	*r = *r.WithContext(context.WithValue(r.Context(), sessionProofKey{}, sessionProof{session: session, role: role}))
	return nil
}

func (s *Server) readSessionFlow(r *http.Request) (*store.Session, error) {
	raw, err := s.sessionPresentation(r)
	if err != nil {
		return nil, err
	}
	if s.store == nil {
		return nil, missingEndpointPolicy()
	}
	if !s.MultiTeam() {
		return nil, sessionUnavailable()
	}
	session, err := s.store.LookupSession(r.Context(), raw)
	if err != nil {
		return nil, sessionFlowError(err)
	}
	if err := s.validateSessionFlow(session); err != nil {
		return nil, err
	}
	return session, nil
}

func (s *Server) validateSessionFlow(session *store.Session) error {
	if session == nil || session.AccountID == "" {
		return sessionUnavailable()
	}
	if s.sessionExpired(session.CreatedAt, time.Now().UTC()) {
		if err := s.store.DeleteSession(session.ID); err != nil {
			return &apierr.Error{Code: apierr.Unavailable, Message: "session service unavailable"}
		}
		return &apierr.Error{Code: apierr.Unauthenticated, Message: errSessionLifetimeExceeded.Error()}
	}
	return nil
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	proof, ok := r.Context().Value(sessionProofKey{}).(sessionProof)
	if !ok || proof.session == nil {
		return missingEndpointPolicy()
	}
	session := proof.session
	writeJSON(w, http.StatusOK, sessionResp{
		Principal: session.Principal, Scopes: append([]string{}, session.Scopes...), CSRFToken: session.CSRFToken,
		ExpiresAt: session.ExpiresAt.Unix(), Team: string(session.Team), Role: string(proof.role), UserID: session.AccountID,
	})
	return nil
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, _ capability.Authorized) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil {
		return invalidEngineBody("session logout takes no body")
	}
	if len(body) != 0 {
		return invalidEngineBody("session logout takes no body")
	}
	if err := s.endVerifiedSession(w, r); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) endVerifiedSession(w http.ResponseWriter, r *http.Request) error {
	proof, ok := r.Context().Value(sessionProofKey{}).(sessionProof)
	if !ok || proof.session == nil {
		return missingEndpointPolicy()
	}
	if err := s.store.DeleteSession(proof.session.ID); err != nil {
		return &apierr.Error{Code: apierr.Unavailable, Message: "session service unavailable"}
	}
	clearSessionCookies(w, s.cookiesSecure())
	return nil
}

func sessionUnavailable() error {
	return &apierr.Error{Code: apierr.Unauthenticated, Message: "session is unavailable"}
}

func sessionFlowError(err error) error {
	if errors.Is(err, store.ErrSessionBackend) {
		return &apierr.Error{Code: apierr.Unavailable, Message: "session service unavailable"}
	}
	return sessionUnavailable()
}

package controller

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

// safety: a refused flow carries only one of these fixed reasons back to the page, never a person, account or
// provider detail; the page holds the sentence for each.
const (
	refusedExpired               = "expired"
	refusedCancelled             = "cancelled"
	refusedEmailUnverified       = "email_unverified"
	refusedAccountExists         = "account_exists"
	refusedNoTeam                = "no_team"
	refusedReauthRequired        = "reauth_required"
	refusedDirectSignIn          = "direct_sign_in_required"
	refusedIdentityElsewhere     = "identity_linked_elsewhere"
	refusedIdentityAlreadyLinked = "identity_already_linked"
	refusedProviderLinked        = "provider_already_linked"
	refusedRateLimited           = "rate_limited"
	refusedUnavailable           = "unavailable"
	refusedFailed                = "failed"
)

var signInRefusalText = map[string]string{
	refusedExpired:               "This sign-in expired or was already used. Sign in again.",
	refusedCancelled:             "Sign-in was cancelled before it finished. Sign in again to continue.",
	refusedEmailUnverified:       "Your sign-in provider has not verified this account's email address. Verify it with the provider, then sign in again.",
	refusedAccountExists:         errAccountExistsSignIn.Error(),
	refusedNoTeam:                "This account belongs to no team yet. Ask a team owner to invite you, then sign in again.",
	refusedReauthRequired:        "Your sign-in could not be confirmed. Sign in again.",
	refusedDirectSignIn:          "Sign in with Google or GitHub to continue.",
	refusedIdentityElsewhere:     "That sign-in belongs to another Sparkwing account. Sign in with that account instead.",
	refusedIdentityAlreadyLinked: "That sign-in is already linked to your account. Sign in with it.",
	refusedProviderLinked:        "Your account already has a sign-in from that provider. Sign in with it.",
	refusedRateLimited:           "Too many sign-in attempts from this network. Wait a minute, then sign in again.",
	refusedUnavailable:           "This sign-in method is unavailable right now. Try again in a minute, or use another sign-in method.",
	refusedFailed:                "Sign-in failed on this server. Try again, and if it keeps failing, contact your Sparkwing administrator.",
}

func reasonedRefusal(code apierr.Code, reason, message string) error {
	return &apierr.Error{Code: code, Message: message, Details: map[string]any{"reason": reason}}
}

func refusalReason(err error) string {
	reason := ""
	if refused, ok := refusalOf(err); ok {
		reason = refused.code
	}
	var failure *apierr.Error
	if reason == "" && errors.As(err, &failure) {
		switch r := failure.Details["reason"].(type) {
		case string:
			reason = r
		case apierr.Reason:
			reason = string(r)
		}
		if reason == "" {
			switch failure.Code {
			case apierr.RateLimited:
				reason = refusedRateLimited
			case apierr.Unauthenticated:
				reason = refusedReauthRequired
			case apierr.Unavailable, apierr.NotFound:
				reason = refusedUnavailable
			}
		}
	}
	switch reason {
	case "link_state_invalid", "provider_rejected":
		return refusedExpired
	case "provider_unverified":
		return refusedEmailUnverified
	}
	if _, known := signInRefusalText[reason]; known {
		return reason
	}
	return refusedFailed
}

type refusedFlowKey struct{}

// safety: a refused browser flow returns the person to a page that says what happened, so a refusal never ends
// on an error body. A server fault is logged here, masked, because the request log records only a 303.
func (s *Server) returnRefusals(e controllerwire.Endpoint, landing func(*http.Request, string) string) controllerwire.Endpoint {
	verify, handle := e.Verify, e.Handle
	e.Verify = func(r *http.Request) error {
		if err := verify(r); err != nil {
			*r = *r.WithContext(context.WithValue(r.Context(), refusedFlowKey{}, err))
		}
		return nil
	}
	e.Handle = func(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
		err, refused := r.Context().Value(refusedFlowKey{}).(error)
		if !refused {
			if err = handle(w, r, a); err == nil {
				return nil
			}
		}
		if refusalStatus(err) >= http.StatusInternalServerError {
			slog.New(CredentialSafeLogHandler(s.logger.Handler())).ErrorContext(r.Context(), "browser flow failed",
				"route", e.Pattern, "err", requestMasker(r.Context()).Mask(err.Error()))
		}
		http.Redirect(w, r, landing(r, refusalReason(err)), http.StatusSeeOther)
		return nil
	}
	return e
}

func refusalStatus(err error) int {
	if refused, ok := refusalOf(err); ok {
		return refused.status
	}
	var failure *apierr.Error
	if errors.As(err, &failure) {
		return apierr.Status(failure.Code)
	}
	return http.StatusInternalServerError
}

func refusedPath(path, reason, next string) string {
	query := url.Values{"refused": {reason}}
	if next != "/" {
		query.Set("next", next)
	}
	return path + "?" + query.Encode()
}

func linkRefusalLanding(_ *http.Request, reason string) string {
	return refusedPath(signInsSettingsPath, reason, "/")
}

func signInStartRefusalLanding(r *http.Request, reason string) string {
	return refusedPath("/login", reason, safeNext(r.URL.Query().Get("next")))
}

func (s *Server) signInCallbackRefusalLanding(r *http.Request, reason string) string {
	flow, ok := s.readOAuthFlow(r)
	if ok && flow.Mode == oauthFlowLink {
		return linkRefusalLanding(r, reason)
	}
	return refusedPath("/login", reason, safeNext(flow.Next))
}

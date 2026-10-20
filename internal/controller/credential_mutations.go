package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// CredentialWriter rechecks live member authority and tenancy in each committing transaction.
type CredentialWriter interface {
	Team() store.Team
	WriteSecret(context.Context, capability.Authorized, store.SealedSecret) error
	RemoveSecret(context.Context, capability.Authorized, string, string) error
	WriteGitCredential(context.Context, capability.Authorized, store.SealedGitCredential) (store.GitCredential, error)
	ConfirmGitCredentialFor(context.Context, capability.Authorized, string, string) (store.GitCredential, error)
	RemoveGitCredential(context.Context, capability.Authorized, string) error
}

// WithCredentialWriter supplies tenant credential transactions before Handler is constructed.
func (s *Server) WithCredentialWriter(resolve func(context.Context, store.Team) (CredentialWriter, error)) *Server {
	s.resolveCredentialWriter = resolve
	return s
}

func (s *Server) credentialWriter(ctx context.Context, team string) (CredentialWriter, error) {
	if team == "" {
		return nil, missingEndpointPolicy()
	}
	var writer CredentialWriter
	var err error
	if s.resolveCredentialWriter != nil {
		writer, err = s.resolveCredentialWriter(ctx, store.Team(team))
	} else if s.store != nil {
		var tenant *store.Tenant
		tenant, err = s.store.ForTeam(ctx, store.Team(team))
		writer, _ = any(tenant).(CredentialWriter)
	}
	if err != nil {
		return nil, engineLookupError(err)
	}
	if writer == nil || writer.Team() != store.Team(team) {
		return nil, missingEndpointPolicy()
	}
	return writer, nil
}

func decodeCredentialRequest[T any](r *http.Request) (*T, error) {
	if err := requireJSONContentType(r.Header.Get("Content-Type")); err != nil {
		return nil, invalidEngineBody("content-type application/json required")
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxSecretJSONBody))
	decoder.DisallowUnknownFields()
	var body *T
	if err := decoder.Decode(&body); err != nil {
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			return nil, &apierr.Error{Code: apierr.PayloadTooLarge, Message: "credential request exceeds the body limit"}
		}
		return nil, invalidEngineBody("invalid credential request")
	}
	if _, err := decoder.Token(); body == nil || !errors.Is(err, io.EOF) {
		return nil, invalidEngineBody("request requires one JSON object")
	}
	return body, nil
}

func credentialWriteError(err error) error {
	switch {
	case errors.Is(err, errBoundCipherRequired):
		return &apierr.Error{Code: apierr.Unavailable, Message: "bound credential encryption is unavailable"}
	case errors.Is(err, secrets.ErrKeyRefused):
		return &apierr.Error{Code: apierr.Conflict, Message: "the secrets key is outside this controller home", Hint: "point SPARKWING_SECRETS_KEY_FILE inside this home"}
	case errors.Is(err, store.ErrSecretLimit):
		return &apierr.Error{Code: apierr.PayloadTooLarge, Message: "secret storage limit exceeded"}
	case errors.Is(err, store.ErrGitCredentialLimit):
		return &apierr.Error{Code: apierr.Conflict, Message: "git credential storage limit exceeded"}
	case errors.Is(err, store.ErrFingerprintMismatch):
		return &apierr.Error{Code: apierr.Conflict, Message: "fingerprint does not match the pinned host key"}
	default:
		return engineLookupError(err)
	}
}

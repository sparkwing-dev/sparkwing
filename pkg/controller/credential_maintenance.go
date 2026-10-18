package controller

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// CredentialRotator rechecks team-admin authority and rotates one tenant's rows in one transaction.
type CredentialRotator interface {
	Team() store.Team
	RotateCredentials(context.Context, capability.Authorized, func(store.Secret) (string, bool, error), func(store.GitCredential) (string, error)) (store.CredentialRotation, error)
}

// SourceAuditReader supplies one tenant's committed source-release audits.
type SourceAuditReader interface {
	Team() store.Team
	SourceReleaseAudits(context.Context, int) ([]capability.SourceReleaseAudit, error)
}

// WithCredentialRotator supplies tenant rotation transactions before Handler is constructed.
func (s *Server) WithCredentialRotator(resolve func(context.Context, store.Team) (CredentialRotator, error)) *Server {
	s.resolveCredentialRotator = resolve
	return s
}

// WithSourceAuditReader supplies tenant source-release reads before Handler is constructed.
func (s *Server) WithSourceAuditReader(resolve func(context.Context, store.Team) (SourceAuditReader, error)) *Server {
	s.resolveSourceAuditReader = resolve
	return s
}

func (s *Server) credentialRotator(ctx context.Context, team string) (CredentialRotator, error) {
	var rotator CredentialRotator
	var err error
	if team == "" {
		return nil, missingEndpointPolicy()
	}
	if s.resolveCredentialRotator != nil {
		rotator, err = s.resolveCredentialRotator(ctx, store.Team(team))
	} else if s.store != nil {
		var tenant *store.Tenant
		tenant, err = s.store.ForTeam(ctx, store.Team(team))
		rotator, _ = any(tenant).(CredentialRotator)
	}
	if err != nil {
		return nil, engineLookupError(err)
	}
	if rotator == nil || rotator.Team() != store.Team(team) {
		return nil, missingEndpointPolicy()
	}
	return rotator, nil
}

func (s *Server) sourceAuditReader(ctx context.Context, team string) (SourceAuditReader, error) {
	var reader SourceAuditReader
	var err error
	if team == "" {
		return nil, missingEndpointPolicy()
	}
	if s.resolveSourceAuditReader != nil {
		reader, err = s.resolveSourceAuditReader(ctx, store.Team(team))
	} else if s.store != nil {
		var tenant *store.Tenant
		tenant, err = s.store.ForTeam(ctx, store.Team(team))
		reader, _ = any(tenant).(SourceAuditReader)
	}
	if err != nil {
		return nil, engineLookupError(err)
	}
	if reader == nil || reader.Team() != store.Team(team) {
		return nil, missingEndpointPolicy()
	}
	return reader, nil
}

func (s *Server) handleRotateSecrets(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil {
		return err
	}
	if len(body) != 0 {
		return invalidEngineBody("credential rotation takes no body")
	}
	cipher, ok := s.secretsCipher.(BoundCipher)
	if !ok {
		return &apierr.Error{Code: apierr.Unavailable, Message: "bound secret encryption is unavailable", Details: map[string]any{"reason": apierr.ReasonSecretsKeyRequired}}
	}
	rotator, err := s.credentialRotator(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	seal := func(binding secretBinding, plain string) (string, error) {
		sealed, err := sealSecret(cipher, binding, plain)
		if err != nil && !errors.Is(err, secrets.ErrKeyRefused) {
			return "", errors.New("credential encryption failed")
		}
		return sealed, err
	}
	secret := func(row store.Secret) (string, bool, error) {
		binding := bindingForRow(rotator.Team(), &row)
		plain, err := openSecret(cipher, binding, row.Value)
		if errors.Is(err, secrets.ErrKeyRefused) {
			return "", false, err
		}
		if err != nil {
			return "", true, nil
		}
		value, err := seal(binding, plain)
		return value, false, err
	}
	git := func(row store.GitCredential) (string, error) {
		if row.Team != rotator.Team() {
			return "", errors.New("invalid rotation tenant")
		}
		binding := gitCredentialBinding(row.Team, row.Host)
		plain, err := openSecret(cipher, binding, row.Secret)
		if errors.Is(err, secrets.ErrKeyRefused) {
			return "", err
		}
		if err != nil {
			return "", errors.New("stored git credential did not open")
		}
		return seal(binding, plain)
	}
	result, err := rotator.RotateCredentials(r.Context(), a, secret, git)
	if err != nil {
		return credentialWriteError(err)
	}
	if result.Rotated < 0 || result.GitCredentials < 0 {
		return errors.New("invalid rotation counts")
	}
	writeJSON(w, http.StatusOK, result)
	return nil
}

func (s *Server) handleRotateSealedValues(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil {
		return err
	}
	if len(body) != 0 {
		return invalidEngineBody("key rotation takes no body")
	}
	query := r.URL.Query()
	verify := query.Has("verify")
	if len(query) > 1 || verify && (len(query["verify"]) != 1 || query.Get("verify") != "1") || len(query) == 1 && !verify {
		return invalidEngineBody("key rotation accepts only verify=1")
	}
	cipher, ok := s.secretsCipher.(store.SealedRotationCipher)
	if !ok || s.store == nil {
		return &apierr.Error{Code: apierr.Unavailable, Message: "bound secret encryption is unavailable", Details: map[string]any{"reason": apierr.ReasonSecretsKeyRequired}}
	}
	result, err := s.store.AsOperator().RotateSealedValues(r.Context(), a, cipher, verify)
	if err != nil {
		return credentialWriteError(err)
	}
	s.logger.Info("sealed values rotated", "verify", verify, "previous_key_only", result.PreviousKeyOnly, "secrets", result.Secrets, "git_credentials", result.GitCredentials,
		"run_environments", result.RunEnvironments, "settings", result.Settings, "unreadable", len(result.Unreadable))
	writeJSON(w, http.StatusOK, result)
	return nil
}

func (s *Server) handleListGitCredentialReleases(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	reader, err := s.sourceAuditReader(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	rows, err := reader.SourceReleaseAudits(r.Context(), gitCredentialListSize)
	if err != nil {
		return engineLookupError(err)
	}
	out := make([]GitCredentialReleaseJSON, 0, len(rows))
	for _, row := range rows {
		if row.Team != a.Target.Team {
			return errors.New("invalid audit tenant")
		}
		out = append(out, GitCredentialReleaseJSON{
			Host: row.Host, Repository: row.RepositoryKey, RunID: row.RunID, JobID: row.NodeID, Generation: row.Generation,
			Runner: row.MachineID, CapabilityPrefix: row.CapabilityPrefix, ReleasedAt: row.ReleasedAt.Unix(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"releases": out})
	return nil
}

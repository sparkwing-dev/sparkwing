package controller

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

// SecretReader resolves a row under live authority and commits claim-release audits atomically.
type SecretReader interface {
	Team() store.Team
	ReadSecret(context.Context, capability.Authorized) (*store.Secret, error)
}

func (s *Server) secretReader(ctx context.Context, team string) (SecretReader, error) {
	return tenantPort[SecretReader](ctx, s, store.Team(team))
}

func secretReadTarget(r *http.Request) (capability.RequestEvidence, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query["run"]) > 1 || len(query["pipeline"]) > 1 {
		return capability.RequestEvidence{}, invalidEngineBody("invalid secret selector")
	}
	name := r.PathValue("name")
	if err := secrets.ValidateName(name); err != nil {
		return capability.RequestEvidence{}, invalidEngineBody("invalid secret name")
	}
	return capability.RequestEvidence{Target: capability.Target{
		SecretName: name, RunID: query.Get("run"), Pipeline: query.Get("pipeline"),
	}}, nil
}

func (s *Server) handleReadSecret(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	reader, err := s.secretReader(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	cipher, ok := s.secretsCipher.(BoundCipher)
	if !ok || cipher == nil {
		return &apierr.Error{Code: apierr.Unavailable, Message: "secret decryption is unavailable"}
	}
	row, err := reader.ReadSecret(r.Context(), a)
	if err != nil {
		return engineLookupError(err)
	}
	if row == nil {
		return engineLookupError(store.ErrNotFound)
	}
	if row.Name != a.Target.SecretName || row.Masked && a.Access.Binding != capability.BindDeclaredSecret {
		return errors.New("invalid persisted secret read")
	}
	if err := r.Context().Err(); err != nil {
		return err
	}
	plain := row.Value
	if !row.Ephemeral {
		plain, err = openSecret(cipher, bindingForRow(reader.Team(), row), row.Value)
		if err != nil {
			return errors.New("stored secret did not open")
		}
	}
	noteAuditTarget(r.Context(), "secret_name", row.Name)
	writeJSON(w, http.StatusOK, controllerwire.SecretValue{
		SecretMetadata: controllerwire.SecretMetadata{
			Name: row.Name, Principal: row.Principal, Pipeline: row.Pipeline,
			Masked: row.Masked, Shared: row.Shared, Bound: secrets.IsBound(row.Value),
			CreatedAt: row.CreatedAt.Unix(), UpdatedAt: row.UpdatedAt.Unix(),
		},
		Value: plain,
	})
	return nil
}

package controller

import (
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type secretSetReq struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Pipeline string `json:"pipeline,omitempty"`
	// safety: nil defaults to masked; only an explicit false stores plain config.
	Masked *bool `json:"masked,omitempty"`
	// safety: an unscoped secret answers a run only when an admin marked it shared.
	Shared bool `json:"shared,omitempty"`
}

type secretJSON struct {
	Name      string `json:"name"`
	Value     string `json:"value,omitempty"`
	Principal string `json:"principal"`
	Pipeline  string `json:"pipeline,omitempty"`
	Masked    bool   `json:"masked"`
	Shared    bool   `json:"shared,omitempty"`
	Bound     bool   `json:"bound"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

func (s *Server) handleCreateSecret(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	req, err := decodeCredentialRequest[secretSetReq](r)
	if err != nil {
		return err
	}
	if err := secrets.ValidateName(req.Name); err != nil {
		return invalidEngineBody("invalid secret name")
	}
	tn, err := s.credentialWriter(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	masked := true
	if req.Masked != nil {
		masked = *req.Masked
	}
	if s.secretsCipher == nil {
		return &apierr.Error{Code: apierr.Unavailable, Message: "secret encryption is unavailable"}
	}
	cipher, ok := s.secretsCipher.(BoundCipher)
	if !ok {
		return credentialWriteError(errBoundCipherRequired)
	}
	stored, err := store.SealSecret(cipher, tn.Team(), store.Secret{
		Name: req.Name, Value: req.Value, Principal: a.Principal.Capability.Subject.ID,
		Pipeline: req.Pipeline, Masked: masked, Shared: req.Shared,
	})
	if err != nil {
		return credentialWriteError(err)
	}
	if err := tn.WriteSecret(r.Context(), a, stored); err != nil {
		return credentialWriteError(err)
	}

	noteAuditTarget(r.Context(), "secret_name", req.Name)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// safety: responses carry credentials or tenant data that browser disk caches
// and intermediaries must not keep.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleListSecrets(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tn, err := s.credentialInventory(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	secs, err := tn.ListSecrets()
	if err != nil {
		return engineLookupError(err)
	}
	out := make([]secretJSON, 0, len(secs))
	for _, sec := range secs {
		var value string
		if !sec.Masked {
			if s.secretsCipher == nil {
				return &apierr.Error{Code: apierr.Unavailable, Message: "secret decryption is unavailable"}
			}
			value, err = openSecret(s.secretsCipher, bindingForRow(tn.Team(), &sec), sec.Value)
			if err != nil {
				return errors.New("stored secret did not open")
			}
		}
		out = append(out, secretJSON{
			Name:      sec.Name,
			Value:     value,
			Principal: sec.Principal,
			Pipeline:  sec.Pipeline,
			Masked:    sec.Masked,
			Shared:    sec.Shared,
			Bound:     secrets.IsBound(sec.Value),
			CreatedAt: sec.CreatedAt.Unix(),
			UpdatedAt: sec.UpdatedAt.Unix(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"secrets": out})
	return nil
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	name := r.PathValue("name")
	tn, err := s.credentialWriter(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	if err := tn.RemoveSecret(r.Context(), a, name, r.URL.Query().Get("pipeline")); err != nil {
		return credentialWriteError(err)
	}
	noteAuditTarget(r.Context(), "secret_name", name)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type secretsRotateResponse struct {
	Rotated        int                 `json:"rotated"`
	Skipped        []secretsRotateSkip `json:"skipped"`
	GitCredentials int                 `json:"git_credentials"`
}

type secretsRotateSkip struct {
	Name     string `json:"name"`
	Pipeline string `json:"pipeline,omitempty"`
}

package controller

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type secretSetReq struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Repo     string `json:"repo,omitempty"`
	Pipeline string `json:"pipeline,omitempty"`
	// OnlyOn is a reserved pool; plan acceptance and release enforce it.
	OnlyOn string `json:"only_on,omitempty"`
	// safety: nil defaults to masked; only an explicit false stores plain config.
	Masked *bool `json:"masked,omitempty"`
	// safety: an unscoped secret answers a run only when an admin marked it shared.
	Shared bool `json:"shared,omitempty"`
}

type secretJSON struct {
	Name      string `json:"name"`
	Value     string `json:"value,omitempty"`
	Principal string `json:"principal"`
	Repo      string `json:"repo,omitempty"`
	RepoKey   string `json:"repo_key,omitempty"`
	Pipeline  string `json:"pipeline,omitempty"`
	OnlyOn    string `json:"only_on,omitempty"`
	Masked    bool   `json:"masked"`
	Shared    bool   `json:"shared,omitempty"`
	Bound     bool   `json:"bound"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// secretRepository resolves the GitHub repository a pipeline secret belongs
// to, as owner/name or its gh: key, to the key a proven run's provenance
// seals. Only runs GitHub proved read pipeline secrets (decision 68), so no
// other host's repository can hold one.
func (s *Server) secretRepository(ctx context.Context, team, repo, pipeline string) (runspec.Repository, error) {
	if (repo == "") != (pipeline == "") {
		return runspec.Repository{}, invalidEngineBody("a pipeline secret belongs to one repository: name both repo and pipeline, or neither for a team-wide secret")
	}
	if repo == "" {
		return runspec.Repository{}, nil
	}
	var request runspec.Request
	if strings.HasPrefix(repo, "gh:") {
		if runspec.ValidateRepoUse(runspec.RepoKey(repo), runspec.RepoAccessRule) != nil {
			return runspec.Repository{}, invalidEngineBody("invalid GitHub repository key")
		}
		request.Git.Repository.Key = runspec.RepoKey(repo)
	} else if _, ok := store.ParseGitHubRepo(repo); ok {
		request.Source.Location.RemoteURL = "https://github.com/" + repo
	} else {
		return runspec.Repository{}, invalidEngineBody("repo names a GitHub repository as owner/name or gh:<id>; only runs GitHub proves read pipeline secrets")
	}
	if err := s.resolveIntakeSource(ctx, team, &request); err != nil {
		return runspec.Repository{}, err
	}
	return request.Git.Repository, nil
}

func (s *Server) handleCreateSecret(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	req, err := decodeCredentialRequest[secretSetReq](r)
	if err != nil {
		return err
	}
	if err := secrets.ValidateName(req.Name); err != nil {
		return invalidEngineBody("invalid secret name")
	}
	if req.OnlyOn != "" && controllerwire.ValidateQueueGroupName(req.OnlyOn) != nil {
		return invalidEngineBody("invalid only_on pool")
	}
	if req.Shared && req.Pipeline != "" {
		return invalidEngineBody("a pipeline secret cannot also be shared team-wide")
	}
	repo, err := s.secretRepository(r.Context(), a.Target.Team, req.Repo, req.Pipeline)
	if err != nil {
		return err
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
		Repo: string(repo.Key), RepoName: repo.Display, Pipeline: req.Pipeline, OnlyOn: req.OnlyOn, Masked: masked, Shared: req.Shared,
	})
	if err != nil {
		return credentialWriteError(err)
	}
	committed, err := tn.WriteSecret(r.Context(), a, stored)
	if err != nil {
		return credentialWriteError(err)
	}

	noteAuditTarget(r.Context(), "secret_name", req.Name)
	response := controllerwire.SecretWritten{Secret: controllerwire.SecretMetadata{
		Name: committed.Name, Principal: committed.Principal,
		Repo: committed.RepoName, RepoKey: committed.Repo, Pipeline: committed.Pipeline, OnlyOn: committed.OnlyOn, Masked: committed.Masked, Shared: committed.Shared, Bound: true,
		CreatedAt: committed.CreatedAt.Unix(), UpdatedAt: committed.UpdatedAt.Unix(),
	}}
	if masked && !secrets.Maskable(req.Value) {
		response.Warning = secrets.ShortValueWarning
	}
	writeJSON(w, http.StatusOK, response)
	return nil
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
			Repo:      sec.RepoName,
			RepoKey:   sec.Repo,
			Pipeline:  sec.Pipeline,
			OnlyOn:    sec.OnlyOn,
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
	repo, err := s.secretRepository(r.Context(), a.Target.Team, r.URL.Query().Get("repo"), r.URL.Query().Get("pipeline"))
	if err != nil {
		return err
	}
	if err := tn.RemoveSecret(r.Context(), a, name, string(repo.Key), r.URL.Query().Get("pipeline")); err != nil {
		return credentialWriteError(err)
	}
	noteAuditTarget(r.Context(), "secret_name", name)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

package controller

import (
	"errors"
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

const accountDeletionCredentialNotice = "Runner and CI tokens you created in shared teams are revoked too, which can stop those integrations. Arrange replacements before deleting."

func (s *Server) handleAccountDeletionPreview(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	writer, err := s.accountWriter()
	if err != nil {
		return err
	}
	plan, err := writer.PreviewAccountDeletion(r.Context(), a)
	if err != nil {
		return accountMutationError(err)
	}
	teams := make([]string, 0, len(plan.Teams))
	for _, team := range plan.Teams {
		teams = append(teams, string(team))
	}
	blocked := make([]string, 0, len(plan.BlockedTeams))
	for _, team := range plan.BlockedTeams {
		blocked = append(blocked, string(team.Slug))
	}
	var archiveNotice string
	for _, entry := range docs.List() {
		if entry.Slug == "account-deletion-retention" {
			archiveNotice = entry.Summary
			break
		}
	}
	if archiveNotice == "" {
		return errors.New("embedded account deletion retention notice is unavailable")
	}
	writeJSON(w, http.StatusOK, controllerwire.AccountDeletionPreview{AccountID: plan.AccountID, Teams: teams, BlockedTeams: blocked, SignInRequired: plan.SignInRequired, ArchiveNotice: archiveNotice, CredentialNotice: accountDeletionCredentialNotice})
	return nil
}

func (s *Server) handleDeleteOwnAccount(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var req controllerwire.AccountDeletionRequest
	if err := decodeEngineRequest(r, &req); err != nil {
		return err
	}
	writer, err := s.accountWriter()
	if err != nil {
		return err
	}
	result, err := writer.DeleteOwnAccount(r.Context(), a, req.AccountID, req.Teams)
	var blocked *store.LastOwnerError
	if errors.As(err, &blocked) {
		teams := make([]string, 0, len(blocked.Teams))
		for _, team := range blocked.Teams {
			teams = append(teams, string(team.Slug))
		}
		return &apierr.Error{Code: apierr.Conflict, Message: "transfer ownership to another member or delete these teams first: " + strings.Join(teams, ", "), Details: map[string]any{"teams": teams}}
	}
	if err != nil {
		return accountMutationError(err)
	}
	clearSessionCookies(w)
	teams := make([]string, 0, len(result.DeletedTeams))
	for _, team := range result.DeletedTeams {
		teams = append(teams, string(team))
	}
	writeJSON(w, http.StatusOK, deletedAccountJSON{AccountID: result.AccountID, DeletedTeams: teams})
	return nil
}

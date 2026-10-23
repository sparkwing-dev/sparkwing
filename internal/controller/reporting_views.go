package controller

import (
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func credentialRotationView(row store.CredentialRotation) controllerwire.CredentialRotation {
	out := controllerwire.CredentialRotation{
		Rotated:        row.Rotated,
		GitCredentials: row.GitCredentials,
	}
	if row.Skipped != nil {
		out.Skipped = make([]controllerwire.SecretRotationSkip, len(row.Skipped))
		for i, row := range row.Skipped {
			out.Skipped[i] = controllerwire.SecretRotationSkip(row)
		}
	}
	return out
}

func sealedRotationView(row store.SealedRotation) controllerwire.SealedRotation {
	out := controllerwire.SealedRotation{
		Secrets:         row.Secrets,
		GitCredentials:  row.GitCredentials,
		RunEnvironments: row.RunEnvironments,
		Settings:        row.Settings,
		PreviousKeyOnly: row.PreviousKeyOnly,
	}
	if row.Unreadable != nil {
		out.Unreadable = make([]controllerwire.SealedRowName, len(row.Unreadable))
		for i, row := range row.Unreadable {
			out.Unreadable[i] = controllerwire.SealedRowName{Purpose: string(row.Purpose), Team: string(row.Team), Name: row.Name, Pipeline: row.Pipeline}
		}
	}
	return out
}

func automationView(row store.GitHubAppAutomation) controllerwire.GitHubAppAutomation {
	out := controllerwire.GitHubAppAutomation{
		RepositoryID:   row.RepositoryID,
		Repository:     row.Repository,
		InstallationID: row.InstallationID,
		Enabled:        row.Enabled,
		EnabledBy:      row.EnabledBy,
		Runner:         string(row.Runner),
	}
	return out
}

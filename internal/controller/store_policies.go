package controller

import (
	"github.com/sparkwing-dev/sparkwing/internal/storage"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// StorePolicies snapshots the configured dependencies for store construction.
func (s *Server) StorePolicies() store.Policies {
	policies := store.Policies{Intake: s, Console: s.consoleAuthority}
	if s.secretsCipher != nil {
		policies.PlanSecrets = planSecretCipher{cipher: s.secretsCipher}
		policies.SessionKeys, _ = s.secretsCipher.(store.SessionKeyCipher)
		policies.RunEnvironment = policies.SessionKeys
	}
	if s.dataController != nil && s.dataController.Objects != nil {
		policies.ResultBlobs = storage.BlobManifests{Objects: s.dataController.Objects}
	}
	return policies
}

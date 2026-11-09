package controller

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (s *Server) handleJobArtifacts(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	if s.dataController == nil || s.dataController.Objects == nil {
		return routepolicy.MissingEndpointPolicy()
	}
	query := r.URL.Query()
	if query.Has("path") {
		if query.Get("path") == "" || len(query["path"]) != 1 || query.Has("limit") || query.Has("cursor") {
			return invalidEngineBody("an exact artifact download accepts one nonempty path without limit or cursor")
		}
	} else if _, _, err := collectionSelection(r, a); err != nil {
		return err
	}
	tenant, err := s.tenantForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return engineLookupError(err)
	}
	manifest, err := tenant.ReadJobManifest(r.Context(), a)
	result := controllerwire.JobArtifacts{Artifacts: []blob.ManifestEntry{}}
	if errors.Is(err, store.ErrNoOutput) && r.URL.Query().Get("path") == "" {
		return writeArtifactPage(w, r, a, result.Artifacts)
	}
	if err != nil {
		return dataRouteError(err)
	}
	members, err := s.dataController.Objects.ReadManifest(r.Context(), manifest.Team, manifest.Digest)
	if err != nil {
		return dataRouteError(err)
	}
	for _, m := range members {
		result.Artifacts = append(result.Artifacts, blob.ManifestEntry{Path: m.Path, Digest: m.Digest, Size: m.Size, Mode: m.Mode})
	}
	slices.SortFunc(result.Artifacts, func(a, b blob.ManifestEntry) int { return strings.Compare(a.Path, b.Path) })
	if path := r.URL.Query().Get("path"); path != "" {
		i := slices.IndexFunc(result.Artifacts, func(e blob.ManifestEntry) bool { return e.Path == path })
		if i < 0 {
			return &apierr.Error{Code: apierr.NotFound, Message: "artifact path is not in the committed job manifest"}
		}
		entry := result.Artifacts[i]
		if err := tenant.ChargeArtifactDownload(r.Context(), a, store.DownloadCharge{Bytes: entry.Size, FreeCapBytes: s.downloadFree, FundedCapBytes: s.downloadFunded}); err != nil {
			return dataRouteError(err)
		}
		object := capability.ObjectToSign{Method: http.MethodGet, Key: "teams/" + manifest.Team + "/cas/" + string(entry.Digest), SHA256: string(entry.Digest), Size: entry.Size, ExpiresAt: time.Now().UTC().Add(limits.DataDownloadURLTTL)}
		signed, err := s.dataController.Objects.Sign(dataDownloadContext(r), object)
		if err != nil {
			return dataRouteError(err)
		}
		if !signedDataMatches(signed, object) {
			return dataProjectionError()
		}
		transfer := dataTransfer(signed)
		result.Artifacts, result.Download = []blob.ManifestEntry{entry}, &transfer
		writeJSON(w, http.StatusOK, result)
		return nil
	}
	return writeArtifactPage(w, r, a, result.Artifacts)
}

func writeArtifactPage(w http.ResponseWriter, r *http.Request, a capability.Authorized, entries []blob.ManifestEntry) error {
	return writeListPage(w, r, a, "artifacts", entries, func(e blob.ManifestEntry) string { return e.Path }, nil)
}

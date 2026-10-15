package controller

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type dataRecordRequest struct {
	Kind           blob.Kind        `json:"kind"`
	Pipeline       string           `json:"pipeline"`
	Job            string           `json:"job"`
	Key            string           `json:"key"`
	Name           string           `json:"name"`
	Digest         blob.Digest      `json:"digest"`
	ManifestDigest *blob.Digest     `json:"manifest_digest,omitempty"`
	Members        []dataMemberJSON `json:"members"`
	ExpiresAt      *time.Time       `json:"expires_at,omitempty"`
}

type dataMemberJSON struct {
	Digest blob.Digest `json:"digest"`
	Path   string      `json:"path"`
	Size   int64       `json:"size"`
	Mode   uint32      `json:"mode"`
}

type dataRecordJSON struct {
	Kind           blob.Kind       `json:"kind"`
	Repo           runspec.RepoKey `json:"repo"`
	Ref            string          `json:"ref"`
	Pipeline       string          `json:"pipeline"`
	Job            string          `json:"job"`
	Key            string          `json:"key"`
	Name           string          `json:"name"`
	Digest         blob.Digest     `json:"digest"`
	ManifestDigest *blob.Digest    `json:"manifest_digest,omitempty"`
	Provenance     blob.Provenance `json:"provenance"`
	CreatedAt      time.Time       `json:"created_at"`
	ExpiresAt      *time.Time      `json:"expires_at,omitempty"`
}

type dataRecordLookupResponse struct {
	Found   bool             `json:"found"`
	Record  *dataRecordJSON  `json:"record,omitempty"`
	Members []dataMemberJSON `json:"members"`
}

func (s *Server) handleDataRecordCommit(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var request dataRecordRequest
	if err := decodeEngineRequest(r, &request); err != nil {
		return err
	}
	if err := validateDataRecordRequest(request); err != nil {
		return err
	}
	binding, err := s.dataController.BindData(r.Context(), a)
	if err != nil {
		return dataRouteError(err)
	}
	authority := binding.Authority
	key := blob.RecordKey{Team: a.Target.Team, Kind: request.Kind, Scope: blob.Scope{Repo: authority.Repo, Ref: authority.Ref.Scope, Pipeline: request.Pipeline, Job: request.Job, Key: request.Key}, Name: request.Name}
	commit := blob.CommitRecordRequest{Authority: authority, Source: authority.Upload.Value, Key: key, Digest: request.Digest}
	if request.ManifestDigest != nil {
		commit.ManifestDigest = runspec.Optional[blob.Digest]{Set: true, Value: *request.ManifestDigest}
	}
	if request.ExpiresAt != nil {
		commit.ExpiresAt = runspec.Optional[time.Time]{Set: true, Value: *request.ExpiresAt}
	}
	manifest := commit.ManifestDigest.Value
	if key.Kind == blob.KindManifest {
		manifest = commit.Digest
	}
	for _, member := range request.Members {
		commit.Members = append(commit.Members, blob.Member{Team: key.Team, ManifestDigest: manifest, Digest: member.Digest, Path: member.Path, Size: member.Size, Mode: member.Mode})
	}
	record, err := binding.Records.CommitRecord(r.Context(), commit)
	if err != nil {
		return dataRouteError(err)
	}
	writeJSON(w, http.StatusOK, dataRecordResponse(record))
	return nil
}

func validateDataRecordRequest(request dataRecordRequest) error {
	if !slices.Contains(blob.Kinds, request.Kind) || request.Name == "" || !validDataDigest(request.Digest) ||
		request.ManifestDigest != nil && !validDataDigest(*request.ManifestDigest) || request.ExpiresAt != nil && request.ExpiresAt.IsZero() {
		return invalidEngineBody("record requires a known kind, name and SHA-256 digests")
	}
	if len(request.Members) != 0 && request.Kind != blob.KindManifest && request.ManifestDigest == nil {
		return invalidEngineBody("record members require a manifest digest")
	}
	for _, member := range request.Members {
		if !validDataDigest(member.Digest) || member.Path == "" || member.Size < 0 {
			return invalidEngineBody("record members require a path, SHA-256 digest and nonnegative size")
		}
	}
	return nil
}

func (s *Server) handleDataRecordLookup(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return invalidEngineBody("invalid record query")
	}
	request, err := dataRecordQuery(query)
	if err != nil {
		return err
	}
	binding, err := s.dataController.BindData(r.Context(), a)
	if err != nil {
		return dataRouteError(err)
	}
	request.Authority, request.Key.Team, request.Key.Scope.Repo = binding.Authority, a.Target.Team, binding.Authority.Repo
	result, err := binding.Records.LookupRecord(r.Context(), request)
	if err != nil {
		return dataRouteError(err)
	}
	response := dataRecordLookupResponse{Found: result.Found, Members: []dataMemberJSON{}}
	if result.Found {
		record := dataRecordResponse(result.Record)
		response.Record = &record
		for _, member := range result.Members {
			response.Members = append(response.Members, dataMemberJSON{Digest: member.Digest, Path: member.Path, Size: member.Size, Mode: member.Mode})
		}
	}
	writeJSON(w, http.StatusOK, response)
	return nil
}

func dataRecordQuery(query url.Values) (blob.RecordLookup, error) {
	allowed := []string{"kind", "ref", "pipeline", "job", "key", "name", "deadline", "verified_default_only"}
	for key, values := range query {
		if !slices.Contains(allowed, key) || len(values) != 1 {
			return blob.RecordLookup{}, invalidEngineBody("unknown or repeated record selector")
		}
	}
	request := blob.RecordLookup{Key: blob.RecordKey{Kind: blob.Kind(query.Get("kind")), Name: query.Get("name"), Scope: blob.Scope{Ref: blob.RefScope{Ref: query.Get("ref")}, Pipeline: query.Get("pipeline"), Job: query.Get("job"), Key: query.Get("key")}}}
	if ref := query.Get("ref"); ref != "" {
		name, manual := strings.CutPrefix(ref, "manual:")
		request.Key.Scope.Ref = blob.RefScope{Ref: name, Verified: !manual}
	}
	if !slices.Contains(blob.Kinds, request.Key.Kind) || request.Key.Name == "" {
		return blob.RecordLookup{}, invalidEngineBody("record query requires a known kind and name")
	}
	if value, present := query["deadline"]; present {
		deadline, err := time.Parse(time.RFC3339Nano, value[0])
		if err != nil {
			return blob.RecordLookup{}, invalidEngineBody("invalid record query deadline")
		}
		request.Budget.Deadline = deadline
	}
	if value, present := query["verified_default_only"]; present {
		verified, err := strconv.ParseBool(value[0])
		if err != nil {
			return blob.RecordLookup{}, invalidEngineBody("invalid verified-default selector")
		}
		request.VerifiedDefaultOnly = verified
	}
	return request, nil
}

func dataRecordResponse(record blob.Record) dataRecordJSON {
	response := dataRecordJSON{Kind: record.Key.Kind, Repo: record.Key.Scope.Repo, Ref: record.Key.Scope.Ref.CapabilityScope(), Pipeline: record.Key.Scope.Pipeline, Job: record.Key.Scope.Job, Key: record.Key.Scope.Key, Name: record.Key.Name, Digest: record.Digest, Provenance: record.Provenance, CreatedAt: record.CreatedAt}
	if record.ManifestDigest.Set {
		response.ManifestDigest = &record.ManifestDigest.Value
	}
	if record.ExpiresAt.Set {
		response.ExpiresAt = &record.ExpiresAt.Value
	}
	return response
}

package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type dataMemoResponse struct {
	Found    bool                        `json:"found"`
	Output   *blob.Digest                `json:"output,omitempty"`
	Manifest *blob.Digest                `json:"manifest,omitempty"`
	Members  []controllerwire.DataMember `json:"members"`
}

type dataDepcacheResponse struct {
	Found  bool                       `json:"found"`
	Record *controllerwire.DataRecord `json:"record,omitempty"`
	Source blob.RestoreSource         `json:"source,omitempty"`
}

func decodeCacheRequest(r *http.Request, required ...string) (map[string]string, blob.Budget, error) {
	raw, err := readEngineBody(r)
	if err != nil {
		return nil, blob.Budget{}, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, blob.Budget{}, invalidEngineBody("cache request requires one JSON object")
	}
	fields := make(map[string]string)
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, blob.Budget{}, invalidEngineBody("invalid cache request")
		}
		key, ok := token.(string)
		_, duplicate := fields[key]
		var value *string
		if !ok || duplicate || key != "deadline" && !slices.Contains(required, key) || d.Decode(&value) != nil || value == nil {
			return nil, blob.Budget{}, invalidEngineBody("cache fields must be unique known strings")
		}
		fields[key] = *value
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') {
		return nil, blob.Budget{}, invalidEngineBody("invalid cache request")
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return nil, blob.Budget{}, invalidEngineBody("cache request requires one JSON object")
	}
	for _, key := range required {
		if fields[key] == "" {
			return nil, blob.Budget{}, invalidEngineBody("cache selectors must be nonempty")
		}
	}
	budget := blob.Budget{}
	if value, exists := fields["deadline"]; exists {
		budget.Deadline, err = time.Parse(time.RFC3339Nano, value)
		if err != nil || budget.Deadline.IsZero() {
			return nil, blob.Budget{}, invalidEngineBody("cache deadline must be a timestamp")
		}
	}
	return fields, budget, nil
}

func (s *Server) handleDataMemo(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	fields, budget, err := decodeCacheRequest(r, "pipeline", "job", "hash")
	if err != nil {
		return err
	}
	binding, err := s.dataController.BindData(r.Context(), a)
	if err != nil {
		return dataRouteError(err)
	}
	lookup := blob.MemoLookup{Authority: binding.Authority, Budget: budget, Key: blob.MemoKey{
		Repo: binding.Authority.Repo, Ref: binding.Authority.Ref.Scope,
		Pipeline: fields["pipeline"], Job: fields["job"], Hash: runspec.Hash(fields["hash"]),
	}}
	var hit blob.MemoHit
	if r.Pattern == "POST /api/v1/data/memo/copy" {
		hit, err = binding.Records.CopyMemo(r.Context(), blob.CopyMemoRequest{Lookup: lookup, Destination: binding.Authority.Attempt.Value})
	} else {
		hit, err = binding.Records.LookupMemo(r.Context(), lookup)
	}
	if err != nil {
		return dataRouteError(err)
	}
	response := dataMemoResponse{Found: hit.Found, Members: []controllerwire.DataMember{}}
	if hit.Found {
		if hit.Output.Set {
			response.Output = &hit.Output.Value
		}
		if hit.Manifest.Set {
			response.Manifest = &hit.Manifest.Value
		}
		for _, member := range hit.Members {
			response.Members = append(response.Members, controllerwire.DataMember{Digest: member.Digest, Path: member.Path, Size: member.Size, Mode: member.Mode})
		}
	}
	writeJSON(w, http.StatusOK, response)
	return nil
}

func (s *Server) handleDataDepcache(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	fields, budget, err := decodeCacheRequest(r, "name", "key")
	if err != nil {
		return err
	}
	binding, err := s.dataController.BindData(r.Context(), a)
	if err != nil {
		return dataRouteError(err)
	}
	hit, err := binding.Records.LookupDepcache(r.Context(), blob.DepcacheLookup{Authority: binding.Authority, Budget: budget, Key: blob.DepcacheKey{
		Repo: binding.Authority.Repo, Ref: binding.Authority.Ref.Scope, Name: fields["name"], Key: fields["key"],
	}})
	if err != nil {
		return dataRouteError(err)
	}
	response := dataDepcacheResponse{Found: hit.Found}
	if hit.Found {
		record := dataRecordResponse(hit.Record)
		response.Record, response.Source = &record, hit.Source
	}
	writeJSON(w, http.StatusOK, response)
	return nil
}

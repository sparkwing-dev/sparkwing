package controller

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func decodeDataTarget(r *http.Request) (capability.RequestEvidence, error) {
	target := capability.DataTarget{}
	var err error
	switch r.Method + " " + r.URL.Path {
	case "POST /api/v1/data/download":
		var body dataDownloadRequest
		err = decodeEngineRequest(r, &body)
		target.Operation, target.Digest = capability.DataDownload, string(body.Digest)
	case "POST /api/v1/data/has-many":
		var body dataPresenceRequest
		err = decodeEngineRequest(r, &body)
		target.Operation = capability.DataHasMany
		if body.Digests != nil {
			target.Digests = make([]string, len(body.Digests))
			for i, digest := range body.Digests {
				target.Digests[i] = string(digest)
			}
		}
	case "POST /api/v1/data/upload":
		var body dataUploadRequest
		err = decodeEngineRequest(r, &body)
		target.Operation, target.Digest = capability.DataUpload, string(body.Digest)
	case "POST /api/v1/data/commit":
		var body dataCommitRequest
		err = decodeEngineRequest(r, &body)
		target.Operation, target.UploadID = capability.DataCommit, body.UploadID
	case "POST /api/v1/data/records":
		var body controllerwire.DataRecordWriteRequest
		err = decodeEngineRequest(r, &body)
		target.Operation, target.Digest, target.RecordKind, target.RecordName = capability.DataRecord, string(body.Digest), string(body.Kind), body.Name
	case "GET /api/v1/data/records":
		var query url.Values
		query, err = url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			err = invalidEngineBody("invalid record query")
		} else {
			body, e := dataRecordQuery(query)
			err = e
			target.Operation, target.RecordKind, target.RecordName, target.RecordScope = capability.DataLookup, string(body.Key.Kind), body.Key.Name, body.Key.Scope.Ref.CapabilityScope()
		}
	case "POST /api/v1/data/memo/lookup", "POST /api/v1/data/memo/copy":
		fields, _, e := decodeCacheRequest(r, "pipeline", "job", "hash")
		err = e
		target.Operation, target.RecordKind, target.RecordName = capability.DataLookup, "memo", fields["hash"]
		if r.URL.Path == "/api/v1/data/memo/copy" {
			target.Operation = capability.DataRecord
		}
	case "POST /api/v1/data/depcache/lookup":
		fields, _, e := decodeCacheRequest(r, "name", "key")
		err = e
		target.Operation, target.RecordKind, target.RecordName = capability.DataLookup, "depcache", fields["name"]
	default:
		err = invalidEngineBody("unknown data operation")
	}
	if err == nil && r.Method == "POST" {
		query, e := url.ParseQuery(r.URL.RawQuery)
		if e != nil {
			return capability.RequestEvidence{}, invalidEngineBody("invalid data query")
		}
		if len(query) != 0 {
			if len(query) != 3 || len(query["job"]) != 1 || len(query["attempt"]) != 1 || len(query["generation"]) != 1 || query.Get("job") == "" {
				return capability.RequestEvidence{}, invalidEngineBody("publication requires job, attempt and generation")
			}
			target.NodeID = query.Get("job")
			target.Attempt, e = strconv.ParseInt(query.Get("attempt"), 10, 64)
			if e != nil || target.Attempt <= 0 {
				return capability.RequestEvidence{}, invalidEngineBody("invalid publication attempt")
			}
			target.Generation, e = strconv.ParseInt(query.Get("generation"), 10, 64)
			if e != nil || target.Generation <= 0 {
				return capability.RequestEvidence{}, invalidEngineBody("invalid publication generation")
			}
		}
	}
	return capability.RequestEvidence{Target: capability.Target{Data: &target}}, err
}

func resolveDataTarget(_ context.Context, p capability.Principal, target capability.Target) (capability.Target, error) {
	if target.Data == nil || p.Capability.Data == nil {
		return target, dataForbidden()
	}
	scope := p.Capability.Data
	if target.Data.NodeID != "" {
		if scope.Use != capability.DataPublish {
			return target, dataForbidden()
		}
		target.Data.RunID = scope.RunID
		return target, nil
	}
	target.Data.RunID, target.Data.NodeID, target.Data.Attempt, target.Data.Generation = scope.RunID, scope.NodeID, scope.Attempt, scope.Generation
	return target, nil
}

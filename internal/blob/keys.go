package blob

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// Valid reports whether d is a lowercase hex SHA-256 digest.
func (d Digest) Valid() bool { return validDigest(d) }

func validDigest(d Digest) bool {
	if len(d) != 64 || strings.ToLower(string(d)) != string(d) {
		return false
	}
	_, err := hex.DecodeString(string(d))
	return err == nil
}

// ObjectKey validates the tenant and digest before constructing the immutable key.
func ObjectKey(team string, digest Digest) (string, error) {
	if team == "" || team == "." || team == ".." || strings.ContainsAny(team, "/\\%") || strings.ContainsFunc(team, unicode.IsControl) || !validDigest(digest) {
		return "", fmt.Errorf("invalid blob team or digest")
	}
	return "teams/" + team + "/cas/" + string(digest), nil
}

func component(s string) (string, error) {
	if s == "" || strings.ContainsFunc(s, unicode.IsControl) {
		return "", fmt.Errorf("empty or invalid record namespace component")
	}
	// safety: encode dot segments too; downstream URL cleaning cannot merge keys.
	return strings.ReplaceAll(url.PathEscape(s), ".", "%2E"), nil
}

func namespace(parts ...string) (string, error) {
	encoded := make([]string, len(parts))
	for i, part := range parts {
		var err error
		encoded[i], err = component(part)
		if err != nil {
			return "", err
		}
	}
	return strings.Join(encoded, "/"), nil
}

func refComponent(ref RefScope) (string, error) {
	if ref.Ref == "" && !ref.Verified {
		return "manual:", nil
	}
	encoded, err := component(ref.Ref)
	if err != nil {
		return "", err
	}
	// safety: escape the colon before applying the trust tag, including literal manual: refs.
	encoded = strings.ReplaceAll(encoded, ":", "%3A")
	if !ref.Verified {
		encoded = "manual:" + encoded
	}
	return encoded, nil
}

func scopedNamespace(kind Kind, repo string, ref RefScope, rest ...string) (string, error) {
	prefix, err := namespace(string(kind), repo)
	if err != nil {
		return "", err
	}
	r, err := refComponent(ref)
	if err != nil {
		return "", err
	}
	suffix, err := namespace(rest...)
	if err != nil {
		return "", err
	}
	return prefix + "/" + r + "/" + suffix, nil
}

func memoNamespace(key MemoKey) (string, error) {
	if !validDigest(Digest(key.Hash)) {
		return "", fmt.Errorf("invalid memo hash")
	}
	return scopedNamespace(KindMemo, string(key.Repo), key.Ref, key.Pipeline, key.Job, string(key.Hash))
}

// DepcacheNamespace encodes a dependency cache scope.
func DepcacheNamespace(key DepcacheKey) (string, error) {
	return scopedNamespace(KindDepcache, string(key.Repo), key.Ref, key.Name, key.Key)
}

// RecordNamespace encodes a logical record scope without delimiter collisions.
func RecordNamespace(key RecordKey) (string, error) {
	if !slices.Contains(Kinds, key.Kind) {
		return "", fmt.Errorf("invalid blob record kind")
	}
	s := key.Scope
	switch key.Kind {
	case KindMemo:
		if !validDigest(Digest(s.Key)) {
			return "", fmt.Errorf("invalid memo hash")
		}
		return scopedNamespace(key.Kind, string(s.Repo), s.Ref, s.Pipeline, s.Job, s.Key)
	case KindDepcache:
		return DepcacheNamespace(DepcacheKey{Repo: s.Repo, Ref: s.Ref, Name: key.Name, Key: s.Key})
	default:
		// safety: optional fields retain their positions so keys cannot alias.
		parts := []string{string(key.Kind), string(s.Repo), s.Ref.Ref, fmt.Sprint(s.Ref.Verified), s.Pipeline, s.Job, s.Key, key.Name}
		for i, p := range parts {
			parts[i] = fmt.Sprintf("%d:%s", len(p), p)
		}
		return namespace(parts...)
	}
}

// ResultRecordKey identifies output or manifest bytes for one exact attempt.
func ResultRecordKey(attempt runspec.AttemptIdentity, repo runspec.RepoKey, ref RefScope, pipeline string, kind Kind) RecordKey {
	return RecordKey{Team: attempt.Team, Kind: kind, Scope: Scope{Repo: repo, Ref: ref, Pipeline: pipeline, Job: attempt.NodeID, Key: fmt.Sprintf("%s/%d/%d", attempt.RunID, attempt.Attempt, attempt.Generation)}, Name: "result"}
}

// ResolveWriteKey derives the record scope from live authority.
func ResolveWriteKey(a Authority, intent WriteScopeIntent) (RecordKey, error) {
	var i UploadIdentity
	if a.Upload.Set {
		i = a.Upload.Value
	} else if a.Attempt.Set {
		i = UploadIdentity{Kind: UploadJob, Team: a.Attempt.Value.Team, RunID: a.Attempt.Value.RunID, Principal: a.Principal, CapabilityRef: a.CapabilityRef, Attempt: a.Attempt}
	}
	if err := AuthorizeUpload(a, i, intent.Kind); err != nil {
		return RecordKey{}, err
	}
	if a.Repo == "" || (a.Ref.Scope.Verified && (a.Ref.AppDeliveryKey == "" || a.Ref.SHA == "")) {
		return RecordKey{}, ErrDigestNotReadable
	}
	if a.Attempt.Set && intent.Job != "" && intent.Job != a.Attempt.Value.NodeID {
		return RecordKey{}, ErrDigestNotReadable
	}
	if intent.Kind == KindOutput || intent.Kind == KindManifest {
		if !i.Attempt.Set || intent.Job != i.Attempt.Value.NodeID || intent.Name != i.RunID || intent.Key != "" {
			return RecordKey{}, ErrDigestNotReadable
		}
		return ResultRecordKey(i.Attempt.Value, a.Repo, a.Ref.Scope, intent.Pipeline, intent.Kind), nil
	}
	key := RecordKey{Team: i.Team, Kind: intent.Kind, Scope: Scope{Repo: a.Repo, Ref: a.Ref.Scope, Pipeline: intent.Pipeline, Job: intent.Job, Key: intent.Key}, Name: intent.Name}
	if _, err := RecordNamespace(key); err != nil {
		return RecordKey{}, err
	}
	return key, nil
}

// MaterializedRecordKey identifies a committed result without inventing an execution attempt.
func MaterializedRecordKey(team, run, job string, seq int64, repo runspec.RepoKey, ref RefScope, pipeline string, kind Kind) RecordKey {
	return RecordKey{Team: team, Kind: kind, Scope: Scope{Repo: repo, Ref: ref, Pipeline: pipeline, Job: job, Key: fmt.Sprintf("%s/result/%d", run, seq)}, Name: "result"}
}

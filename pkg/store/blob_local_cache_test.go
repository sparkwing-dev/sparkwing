package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestLocalDepcacheRoundTrip(t *testing.T) {
	for _, ref := range []string{"", "main"} {
		t.Run(ref, func(t *testing.T) {
			s, records, intent := blobRecordFixture(t)
			digest := sha256.Sum256([]byte(t.Name()))
			repo := runspec.RepoKey("local:" + hex.EncodeToString(digest[:]))
			rows, err := s.db.Query("SELECT authority_json FROM capabilities")
			if err != nil {
				t.Fatal(err)
			}
			var capabilities []CapabilityRecord
			for rows.Next() {
				var body []byte
				if err := rows.Scan(&body); err != nil {
					t.Fatal(err)
				}
				var c CapabilityRecord
				if err := json.Unmarshal(body, &c); err != nil {
					t.Fatal(err)
				}
				capabilities = append(capabilities, c)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
			for _, c := range capabilities {
				c.Value.Subject.Kind = capability.SubjectLocal
				dataRepo, dataRef := "", ""
				if c.Value.Data != nil {
					c.Value.Data.RepositoryKey = string(repo)
					c.Value.Data.WriteRef = fixtureRef(ref).CapabilityScope()
					c.Value.Data.ReadRefs = []string{c.Value.Data.WriteRef}
					dataRepo, dataRef = string(repo), c.Value.Data.WriteRef
				}
				body, err := json.Marshal(c)
				if err != nil {
					t.Fatal(err)
				}
				rewriteSchemaExec(t, s.db, "UPDATE capabilities SET subject_kind=?,repo_key=?,ref_scope=?,authority_json=? WHERE prefix=?", c.Value.Subject.Kind, dataRepo, dataRef, body, c.Value.Prefix)
			}
			body := fixtureSeal(t, "ci", runspec.SealedRequest{Request: runspec.Request{Origin: runspec.OriginLocal, Git: runspec.Git{Ref: ref}}})
			rewriteSchemaExec(t, s.db, "UPDATE runs SET repo_key=?,request_json=? WHERE team='a' AND id='run'", repo, body)
			intent.Authority.Repo, intent.Authority.Ref.Scope = repo, fixtureRef(ref)
			authority, err := records.tenant.ResolveBlobAuthority(t.Context(), intent.Authority, capability.DataWrite)
			if err != nil {
				t.Fatal(err)
			}
			intent.Authority = authority
			if _, err := commitTestBlob(t, records.tenant, intent); err != nil {
				t.Fatal(err)
			}
			key := blob.RecordKey{Team: "a", Kind: blob.KindDepcache, Scope: blob.Scope{Repo: repo, Ref: fixtureRef(ref), Key: "v1"}, Name: "modules"}
			if _, err := records.CommitRecord(t.Context(), blob.CommitRecordRequest{Authority: authority, Source: intent.Identity, Key: key, Digest: intent.Digest}); err != nil {
				t.Fatal(err)
			}
			intent.Authority.Upload = runspec.Optional[blob.UploadIdentity]{}
			reader := addBlobJob(t, s, intent, "consumer")
			hit, err := records.LookupDepcache(t.Context(), blob.DepcacheLookup{Authority: reader.Authority, Key: blob.DepcacheKey{Repo: repo, Ref: key.Scope.Ref, Name: key.Name, Key: key.Scope.Key}})
			if err != nil || !hit.Found || hit.Record.Digest != intent.Digest {
				t.Fatalf("local cache=%+v error=%v", hit, err)
			}
			if metadata, err := records.tenant.ReadBlob(t.Context(), reader.Authority, intent.Digest); err != nil || metadata.Digest != intent.Digest {
				t.Fatalf("consumer cannot read local cache bytes: %+v %v", metadata, err)
			}
			if err := quick.Check(func(value []byte) bool {
				foreign := reader.Authority
				digest := sha256.Sum256(value)
				foreign.Repo += runspec.RepoKey("foreign-" + hex.EncodeToString(digest[:]))
				hit, err := records.LookupDepcache(t.Context(), blob.DepcacheLookup{Authority: foreign, Key: blob.DepcacheKey{Repo: foreign.Repo, Ref: key.Scope.Ref, Name: key.Name, Key: key.Scope.Key}})
				return err != nil && !hit.Found
			}, &quick.Config{MaxCount: 16}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocalDepcacheWriteScopeProperty(t *testing.T) {
	property := func(input []byte, ref string) bool {
		if len(ref) > 128 {
			return true
		}
		digest := sha256.Sum256(input)
		repo := runspec.RepoKey("local:" + hex.EncodeToString(digest[:]))
		scope := blob.RefScope{Ref: ref}
		a := blobActor{capability: capability.Capability{Team: "a", Subject: capability.Subject{Kind: capability.SubjectLocal}, Data: &capability.DataScope{Use: capability.DataClaim, WriteRef: scope.CapabilityScope()}}, authority: blob.Authority{Repo: repo, Ref: blob.RefFacts{Scope: scope}}}
		key := blob.RecordKey{Team: "a", Kind: blob.KindDepcache, Scope: blob.Scope{Repo: repo, Ref: scope, Key: "key"}, Name: "modules"}
		if checkBlobWriteKey(a, key) != nil {
			return false
		}
		for _, kind := range []capability.SubjectKind{capability.SubjectMachine, capability.SubjectAccount} {
			other := a
			other.capability.Subject.Kind = kind
			if checkBlobWriteKey(other, key) == nil {
				return false
			}
		}
		for _, use := range []capability.DataUse{capability.DataPublish, capability.DataRemoteCache, capability.DataExpansion} {
			other := a
			data := *a.capability.Data
			data.Use = use
			other.capability.Data = &data
			if checkBlobWriteKey(other, key) == nil {
				return false
			}
		}
		key.Scope.Repo += "foreign"
		return checkBlobWriteKey(a, key) != nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
}

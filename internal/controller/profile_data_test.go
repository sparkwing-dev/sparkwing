package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestProfileCacheCredentialFromOrdinaryMember(t *testing.T) {
	s, session := credentialSessionServer(t)
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "https://objects.test", SigningKey: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	s.WithDataObjects(objects)
	h := s.Handler()
	w := credentialRequestHTTP(t, h, session, `{"grants":["runs.read","runs.write"]}`, false)
	var root credentialResponse
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &root) != nil {
		t.Fatalf("profile root: %d %s", w.Code, w.Body.String())
	}
	w = credentialRequestHTTP(t, h, root.Capability, `{"grants":["data.read"],"data":{"use":"remote_cache","repo_key":"gh:42","read_refs":["main"],"verified_default_only":true}}`, true)
	var reader credentialResponse
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &reader) != nil {
		t.Fatalf("profile cache reader: %d %s", w.Code, w.Body.String())
	}
	p, err := s.store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(reader.Capability)))
	if err != nil || p.Capability.Parent == nil || p.Capability.Parent.ID != root.Prefix || p.Capability.Data == nil || !p.Capability.Data.VerifiedDefaultOnly || p.Capability.Data.RepositoryKey != "gh:42" || !slices.Equal(p.Capability.Grants, []capability.Grant{capability.DataRead}) {
		t.Fatalf("scoped cache credential: %+v %v", p, err)
	}
	profile, err := client.OpenProfileData(t.Context(), client.EngineOptions{URL: "https://controller.test", HTTP: &http.Client{Transport: localHostRoundTripper{h}}, Bearer: func(context.Context) (string, error) { return root.Capability, nil }}, capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: "gh:42", ReadRefs: []string{"main"}, VerifiedDefaultOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	cache, err := profile.RemoteCache("ci")
	if err != nil {
		t.Fatal(err)
	}
	hit, err := cache.Memo(t.Context(), blob.MemoKey{Repo: "gh:42", Pipeline: "ci", Job: "build", Hash: runspec.Hash(strings.Repeat("a", 64))}, blob.Budget{Deadline: time.Now().Add(time.Minute)})
	if err != nil || hit.Found {
		t.Fatalf("profile-backed cache miss: %+v %v", hit, err)
	}
	if err := profile.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	engineSQL(t, s.store, `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, root.Prefix)
	if _, err := s.store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(reader.Capability))); err == nil {
		t.Fatal("reader escaped profile revocation")
	}
}

func TestProfileCacheCredentialNeverWidensAccountAuthority(t *testing.T) {
	s, session := credentialSessionServer(t)
	h := s.Handler()
	property := func(selector uint8, value string) bool {
		data := capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: "gh:42", ReadRefs: []string{"main"}, VerifiedDefaultOnly: true}
		grants := []capability.Grant{capability.DataRead}
		switch selector % 8 {
		case 0:
			data.VerifiedDefaultOnly = false
		case 1:
			data.WriteRef = "manual:" + value
		case 2:
			data.RunID = "foreign:" + value
		case 3:
			data.NodeID = "foreign:" + value
		case 4:
			data.Attempt = 1
		case 5:
			data.Generation = 1
		case 6:
			grants = append(grants, capability.DataWrite)
		case 7:
			data.RepositoryKey = ""
		}
		body, err := json.Marshal(map[string]any{"grants": grants, "data": data})
		if err != nil {
			t.Fatal(err)
		}
		w := credentialRequestHTTP(t, h, session, string(body), false)
		return w.Code >= 400 && w.Code < 500
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
}

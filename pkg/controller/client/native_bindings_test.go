package client

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func FuzzNativeDataBindingsRejectForeignAuthority(f *testing.F) {
	for i := uint8(0); i < 6; i++ {
		f.Add(i, "foreign")
	}
	f.Fuzz(func(t *testing.T, field uint8, value string) {
		identity := runspec.AttemptIdentity{Team: "team", RunID: "run", NodeID: "job", Generation: 2, Attempt: 1}
		authority := blob.Authority{CapabilityRef: "award", Principal: "principal", Repo: "repo", Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: identity}}
		foreign := authority
		switch field % 6 {
		case 0:
			foreign.CapabilityRef += "-" + value
		case 1:
			foreign.Principal += blob.Principal("-" + value)
		case 2:
			foreign.Repo += runspec.RepoKey("-" + value)
		case 3:
			foreign.Attempt.Value.Generation++
		case 4:
			foreign.Attempt.Value.Attempt++
		case 5:
			foreign.Ref.Scope.Ref += "-" + value
		}
		data := &dataClient{authority: &authority}
		var err error
		switch field % 6 {
		case 0:
			_, err = data.Upload(t.Context(), blob.UploadIntent{Authority: foreign})
		case 1:
			_, err = data.Commit(t.Context(), blob.CommitUpload{Authority: foreign})
		case 2:
			_, err = data.Download(t.Context(), blob.GetRequest{Authority: foreign})
		case 3:
			_, err = data.HasMany(t.Context(), blob.HasManyRequest{Authority: foreign})
		case 4:
			_, err = data.LookupRecord(t.Context(), blob.RecordLookup{Authority: foreign})
		case 5:
			_, err = data.CommitRecord(t.Context(), blob.CommitRecordRequest{Authority: foreign})
		}
		if err == nil {
			t.Fatal("foreign authority reached the unconfigured transport")
		}
		if _, err := data.LookupMemo(t.Context(), blob.MemoLookup{Authority: foreign}); err == nil {
			t.Fatal("foreign memo authority reached the unconfigured transport")
		}
		if _, err := data.CopyMemo(t.Context(), blob.CopyMemoRequest{Lookup: blob.MemoLookup{Authority: foreign}, Destination: foreign.Attempt.Value}); err == nil {
			t.Fatal("foreign memo copy authority reached the unconfigured transport")
		}
	})
}

func TestNativeCachesRequireBoundSelectors(t *testing.T) {
	data := &dataClient{}
	if _, err := data.LookupMemo(t.Context(), blob.MemoLookup{}); err == nil {
		t.Fatal("unbound memo reached transport")
	}
	if _, err := data.LookupDepcache(t.Context(), blob.DepcacheLookup{}); err == nil {
		t.Fatal("unbound depcache reached transport")
	}
	if _, err := data.CopyMemo(t.Context(), blob.CopyMemoRequest{}); err == nil {
		t.Fatal("unbound memo copy reached transport")
	}
}

package capability

import (
	"slices"
	"testing"
	"testing/quick"
)

func TestProfileDataGrantsFollowLiveAccountAuthority(t *testing.T) {
	property := func(read, write, publish, restricted bool) bool {
		parent := Principal{Capability: Capability{Subject: Subject{Kind: SubjectAccount, ID: "account"}, Audience: Controller, Binding: Binding{Kind: BindingNone}}}
		if read {
			parent.Capability.Grants = append(parent.Capability.Grants, RunsRead)
		}
		if write {
			parent.Capability.Grants = append(parent.Capability.Grants, RunsWrite)
		}
		if restricted {
			parent.Capability.Repositories = []string{"other"}
		}
		child := Capability{Audience: Controller, Binding: Binding{Kind: BindingNone}, Repositories: []string{"repo"}, Grants: []Grant{DataRead}, Data: &DataScope{Use: DataRemoteCache, RepositoryKey: "repo", ReadRefs: []string{"main"}, VerifiedDefaultOnly: true}}
		grant, allowed := DataRead, read && !restricted
		if publish {
			child.Binding = Binding{Kind: BindingRun, RunID: "publication"}
			child.Data = &DataScope{Use: DataPublish, RepositoryKey: "repo", RunID: "publication", WriteRef: "manual:main", ReadRefs: []string{"manual:main"}}
			grant, allowed = DataWrite, write && !restricted
			child.Grants = []Grant{grant}
		}
		return slices.Contains(InheritedGrants(parent, child), grant) == allowed
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}

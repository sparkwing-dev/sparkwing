package main

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestSchemaVerdict_FollowsTheRequirementStamps(t *testing.T) {
	req := func(name, addedBy string) store.SchemaRequirement {
		return store.SchemaRequirement{Name: name, AddedBy: addedBy}
	}
	cases := []struct {
		name      string
		pin       string
		replace   string
		listed    []store.SchemaRequirement
		wantOpens bool
		wantNeeds string
		wantNote  string
	}{
		{
			name: "requirement added after the pin", pin: "v0.39.0",
			listed:    []store.SchemaRequirement{req("unique-token-prefix", "v0.38.0"), req("webhook-replay-keys", "v0.41.0")},
			wantOpens: false, wantNeeds: "v0.41.0",
			wantNote: "database uses webhook-replay-keys, added after pin v0.39.0; a run would be refused",
		},
		{
			name: "pin at the newest stamp", pin: "v0.41.0",
			listed:    []store.SchemaRequirement{req("unique-token-prefix", "v0.38.0"), req("webhook-replay-keys", "v0.41.0")},
			wantOpens: true,
			wantNote:  "pin v0.41.0 knows every requirement the database records",
		},
		{
			name: "pin ahead of every stamp", pin: "v0.42.0",
			listed:    []store.SchemaRequirement{req("unique-token-prefix", "v0.38.0")},
			wantOpens: true,
			wantNote:  "pin v0.42.0 knows every requirement the database records",
		},
		{
			name: "development stamp is not a verdict", pin: "v0.39.0",
			listed:    []store.SchemaRequirement{req("inherited-holder-marker", "(devel)")},
			wantOpens: true,
			wantNote:  "database uses inherited-holder-marker, stamped by a development build, so whether pin v0.39.0 knows them cannot be told from the stamp",
		},
		{
			name: "replaced sdk", replace: "../sdk",
			listed:    []store.SchemaRequirement{req("webhook-replay-keys", "v0.41.0")},
			wantOpens: true,
			wantNote:  "SDK replaced with a local module; schema compatibility depends on that checkout",
		},
		{
			name:      "no pin",
			listed:    []store.SchemaRequirement{req("webhook-replay-keys", "v0.41.0")},
			wantOpens: true,
			wantNote:  "no SDK pin resolved for this repo",
		},
		{
			name: "database records no requirements", pin: "v0.16.0",
			wantOpens: true,
			wantNote:  "database records no schema requirements; any pin may open it",
		},
		{
			name: "uncomparable pin", pin: "not-a-version",
			listed:    []store.SchemaRequirement{req("webhook-replay-keys", "v0.41.0")},
			wantOpens: true,
			wantNote:  "pin not-a-version is not a comparable version",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opens, needs, note := schemaVerdict(tc.pin, tc.replace, tc.listed)
			if opens != tc.wantOpens {
				t.Errorf("opens = %v, want %v", opens, tc.wantOpens)
			}
			if needs != tc.wantNeeds {
				t.Errorf("needs = %q, want %q", needs, tc.wantNeeds)
			}
			if note != tc.wantNote {
				t.Errorf("note =\n%s\nwant\n%s", note, tc.wantNote)
			}
		})
	}
}

func TestRepoSuggestion_LeadsWithSchemaThenGuidesThenDirty(t *testing.T) {
	schemaBlocked := repoInfo{
		Pin:          "v0.16.0",
		Latest:       "v0.17.0",
		GuidesBehind: 2,
		Dirty:        true,
		Schema:       repoSchema{PinOpensDB: false, NeedsVersion: "v0.17.0"},
	}
	if got := repoSuggestion(schemaBlocked); !strings.Contains(got, "cannot open") || !strings.Contains(got, "v0.17.0") {
		t.Fatalf("schema-blocked suggestion = %q, want the DB fix first", got)
	}

	behind := repoInfo{Pin: "v0.16.0", Latest: "v0.17.0", GuidesBehind: 2, Schema: repoSchema{PinOpensDB: true}}
	if got := repoSuggestion(behind); !strings.Contains(got, "2 guide(s) behind") {
		t.Fatalf("behind suggestion = %q, want the guides bump", got)
	}

	dirty := repoInfo{Pin: "v0.17.0", Latest: "v0.17.0", Dirty: true, Schema: repoSchema{PinOpensDB: true}}
	if got := repoSuggestion(dirty); !strings.Contains(got, "uncommitted") {
		t.Fatalf("dirty suggestion = %q, want the dirty-tree note", got)
	}

	clean := repoInfo{Pin: "v0.17.0", Latest: "v0.17.0", Schema: repoSchema{PinOpensDB: true}}
	if got := repoSuggestion(clean); got != "" {
		t.Fatalf("clean suggestion = %q, want empty", got)
	}
}

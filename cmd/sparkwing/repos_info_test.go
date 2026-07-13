package main

import (
	"strings"
	"testing"
)

func TestSchemaVerdict_PinBelowMinimumCannotOpen(t *testing.T) {
	cases := []struct {
		name       string
		pin        string
		replace    string
		minVersion string
		wantOpens  bool
	}{
		{"below minimum", "v0.16.0", "", "v0.17.0", false},
		{"at minimum", "v0.17.0", "", "v0.17.0", true},
		{"above minimum", "v0.18.0", "", "v0.17.0", true},
		{"replaced sdk", "", "../sdk", "v0.17.0", true},
		{"no pin", "", "", "v0.17.0", true},
		{"no stamp", "v0.16.0", "", "", true},
		{"uncomparable stamp", "v0.16.0", "", "(devel)", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, note := schemaVerdict(tc.pin, tc.replace, tc.minVersion)
			if got != tc.wantOpens {
				t.Fatalf("schemaVerdict(%q,%q,%q) opens = %v, want %v", tc.pin, tc.replace, tc.minVersion, got, tc.wantOpens)
			}
			if note == "" {
				t.Fatalf("schemaVerdict returned an empty note")
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
		Schema:       repoSchema{PinOpensDB: false, MinVersion: "v0.17.0"},
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

package argvalue

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func TestListUsesUnicodeWhitespaceAndOmitsEmptyItems(t *testing.T) {
	got := List("\u00a0first\u0085, ,\u2007second\u202f,\u3000")
	if !slices.Equal(got, []string{"first", "second"}) {
		t.Fatalf("list = %q", got)
	}
}

func TestListMatchesSharedSDKNormalizationContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/lists.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Input string
		Want  []string
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		if got := List(tc.Input); !slices.Equal(got, tc.Want) {
			t.Fatalf("List(%q)=%q, want %q", tc.Input, got, tc.Want)
		}
	}
}

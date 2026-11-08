package docsweb

import (
	"encoding/json"
	"os"
	"testing"
)

// safety: construct cases catch interactions absent from shipped pages.
func TestRendererConformance(t *testing.T) {
	data, err := os.ReadFile("testdata/renderer-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string
		Requires string
		Markdown string
		HTML     string
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("empty renderer corpus")
	}
	supported := map[string]bool{"authority-rejection": true, "common": true, "full": true, "wrapped": true}
	names := make(map[string]bool)
	for _, tc := range cases {
		if tc.Name == "" || names[tc.Name] {
			t.Fatalf("empty or duplicate case name %q", tc.Name)
		}
		names[tc.Name] = true
		switch tc.Requires {
		case "common", "full", "wrapped", "reduced", "reduced-wrapped", "authority-rejection":
		default:
			t.Fatalf("unknown feature %q in %s", tc.Requires, tc.Name)
		}
		t.Run(tc.Name, func(t *testing.T) {
			if !supported[tc.Requires] {
				t.Skip("renderer does not support " + tc.Requires)
			}
			if got := string(renderMarkdown(tc.Markdown, noLinks)); got != tc.HTML {
				t.Errorf("Markdown: %q\ngot:  %q\nwant: %q", tc.Markdown, got, tc.HTML)
			}
		})
	}
}

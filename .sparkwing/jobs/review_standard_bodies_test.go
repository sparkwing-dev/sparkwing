package jobs

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReviewStandardBodiesKeepRulesAcrossCards(t *testing.T) {
	cards := []string{
		"---\napply_type: [review]\n---\n\n# Rules\n\n1. Keep every rule.\n\n---\n\n2. Keep thematic breaks.\n\n## Related\n\n- [[other]] \u2014 semantic\n",
		"# Other\n\nKeep this too.\n",
	}
	var input strings.Builder
	for _, contents := range cards {
		if err := json.NewEncoder(&input).Encode(map[string]string{"contents": contents}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := reviewStandardBodies(input.String())
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{"# Rules", "1. Keep every rule.", "---", "2. Keep thematic breaks.", "# Other", "Keep this too."} {
		if !strings.Contains(got, rule) {
			t.Errorf("missing rule %q in %s", rule, got)
		}
	}
	for _, metadata := range []string{"apply_type", "[[other]]", "## Related"} {
		if strings.Contains(got, metadata) {
			t.Errorf("retained metadata %q", metadata)
		}
	}
}

func TestReviewStandardBodiesPreserveRelatedProse(t *testing.T) {
	body := "---\nlint_rules: [preserve]\n---\n# Rules\n\n## Related\n\n- [[other]] must be read before review.\n"
	raw, err := json.Marshal(map[string]string{"contents": body})
	if err != nil {
		t.Fatal(err)
	}
	got, err := reviewStandardBodies(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.TrimSpace(body) {
		t.Fatalf("lost prose: %s", got)
	}
	if _, err := reviewStandardBodies(string(raw) + " invalid"); err == nil {
		t.Fatal("accepted incomplete standards")
	}
}

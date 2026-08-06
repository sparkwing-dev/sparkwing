package docs_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

// A guide that names a renamed or deleted topic is worse than no guide:
// it fails at the moment someone is trying to learn something.
func TestGuideTopicsAllResolve(t *testing.T) {
	for _, g := range docs.Guides() {
		if len(g.Topics) == 0 {
			t.Errorf("guide %q lists no topics", g.Name)
		}
		if strings.TrimSpace(g.Summary) == "" {
			t.Errorf("guide %q has no summary", g.Name)
		}
		for _, slug := range g.Topics {
			if _, err := docs.Read(slug); err != nil {
				t.Errorf("guide %q names topic %q, which does not resolve: %v", g.Name, slug, err)
			}
		}
	}
}

// Guides carry narrative topics. The generated references are lookup
// tables in the tens of thousands of tokens; bundling one would make
// every guide read expensive to deliver something nobody reads end to
// end. `docs search` is the path to those.
func TestGuidesExcludeGeneratedReferences(t *testing.T) {
	banned := map[string]bool{"sdk-reference": true, "cli-reference": true, "changelog": true}
	for _, g := range docs.Guides() {
		for _, slug := range g.Topics {
			if banned[slug] {
				t.Errorf("guide %q includes generated reference %q", g.Name, slug)
			}
		}
	}
}

func TestReadGuideConcatenatesInOrder(t *testing.T) {
	g, ok := docs.GuideByName("authoring")
	if !ok {
		t.Fatal("expected an 'authoring' guide")
	}
	body, err := docs.ReadGuide("authoring")
	if err != nil {
		t.Fatalf("ReadGuide: %v", err)
	}
	prev := -1
	for _, slug := range g.Topics {
		at := strings.Index(body, "# DOC: "+slug+"\n")
		if at < 0 {
			t.Fatalf("guide body is missing topic %q", slug)
		}
		if at < prev {
			t.Errorf("topic %q appears out of declaration order", slug)
		}
		prev = at
	}
	if !strings.Contains(body, "<!-- guide: authoring") {
		t.Error("guide body should name itself for a reader that piped it somewhere")
	}
}

func TestReadGuideUnknownNamesTheAlternatives(t *testing.T) {
	_, err := docs.ReadGuide("nope")
	if err == nil {
		t.Fatal("expected an error for an unknown guide")
	}
	for _, name := range docs.GuideNames() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q should list the available guide %q", err, name)
		}
	}
}

func TestGuideByNameIsCaseInsensitive(t *testing.T) {
	if _, ok := docs.GuideByName("AUTHORING"); !ok {
		t.Error("guide lookup should not care about case")
	}
}

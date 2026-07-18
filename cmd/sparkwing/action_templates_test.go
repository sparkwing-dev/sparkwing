package main

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/color"

	templates "github.com/sparkwing-dev/sparks-core/templates"
)

func tmplWithCategory(name, category string) templates.Template {
	return templates.Template{Manifest: templates.Manifest{
		Name:          name,
		Applicability: templates.Applicability{Category: category},
	}}
}

func TestGroupTemplatesByCategory_SortsAlphabeticallyUncategorizedLast(t *testing.T) {
	in := []templates.Template{
		tmplWithCategory("z-build", "build"),
		tmplWithCategory("loose", ""),
		tmplWithCategory("deploy-a", "deploy"),
		tmplWithCategory("a-build", "build"),
	}
	groups := groupTemplatesByCategory(in)
	var order []string
	for _, g := range groups {
		order = append(order, g.category)
	}
	want := []string{"build", "deploy", uncategorizedLabel}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("category order = %v, want %v", order, want)
	}
}

func TestGroupTemplatesByCategory_PreservesOrderWithinBucket(t *testing.T) {
	in := []templates.Template{
		tmplWithCategory("first", "build"),
		tmplWithCategory("second", "build"),
		tmplWithCategory("third", "build"),
	}
	groups := groupTemplatesByCategory(in)
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	var names []string
	for _, tmpl := range groups[0].templates {
		names = append(names, tmpl.Manifest.Name)
	}
	if got := strings.Join(names, ","); got != "first,second,third" {
		t.Fatalf("within-bucket order = %q, want first,second,third", got)
	}
}

func TestRenderTemplateList_PrintsCategoryHeadersAndFooter(t *testing.T) {
	color.SetEnabled(false)
	in := []templates.Template{
		tmplWithCategory("alpha", "build"),
		tmplWithCategory("bravo", "deploy"),
	}
	out := captureStdout(t, func() { renderTemplateList(in) })

	for _, header := range []string{"BUILD", "DEPLOY"} {
		if !strings.Contains(out, header) {
			t.Errorf("output missing category header %q\n%s", header, out)
		}
	}
	for _, affordance := range []string{
		"shown:", "2 templates across 2 categories",
		"filter:", "--category <category> --cloud <aws|gcp>",
		"detail:", "--name <template> [--body]",
		"scaffold:", "sparkwing pipeline new",
	} {
		if !strings.Contains(out, affordance) {
			t.Errorf("footer missing %q\n%s", affordance, out)
		}
	}
	if strings.Index(out, "BUILD") > strings.Index(out, "shown:") {
		t.Errorf("footer should come after the listing\n%s", out)
	}
}

func TestRenderTemplateList_SingularCountsForOneTemplate(t *testing.T) {
	color.SetEnabled(false)
	in := []templates.Template{tmplWithCategory("solo", "build")}
	out := captureStdout(t, func() { renderTemplateList(in) })
	if !strings.Contains(out, "1 template across 1 category") {
		t.Errorf("expected singular counts, got:\n%s", out)
	}
}

func TestGroupTemplatesByCategory_TrimsAndBucketsBlankCategory(t *testing.T) {
	in := []templates.Template{
		tmplWithCategory("padded", "  build  "),
		tmplWithCategory("blank", "   "),
	}
	groups := groupTemplatesByCategory(in)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(groups))
	}
	if groups[0].category != "build" {
		t.Errorf("first category = %q, want build", groups[0].category)
	}
	if groups[1].category != uncategorizedLabel {
		t.Errorf("second category = %q, want %q", groups[1].category, uncategorizedLabel)
	}
}

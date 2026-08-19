package orchestrator

import (
	"go/parser"
	"go/token"
	"regexp"
	"strings"
	"testing"
)

var triggerConsumerReviewLabels = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\badversarial review\b`),
	regexp.MustCompile(`(?i)\bthe review\b`),
	regexp.MustCompile(`(?i)\bblocker(?:\s+[0-9]+)?\b`),
	regexp.MustCompile(`\bTest[A-Za-z0-9_]+\s+is\s+S[0-9]+\.`),
}

func TestTriggerConsumerCommentsDoNotCarryReviewLabels(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "trigger_consumer_test.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range file.Comments {
		text := strings.TrimSpace(group.Text())
		for _, pattern := range triggerConsumerReviewLabels {
			if pattern.MatchString(text) {
				t.Errorf("comment carries review-only wording %q: %s", pattern, text)
			}
		}
	}
}

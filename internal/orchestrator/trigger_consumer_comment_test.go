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
	regexp.MustCompile(`(?i)\bthe review (?:saw|ran|reproduced)\b`),
	regexp.MustCompile(`\bTest[A-Za-z0-9_]+\s+is\s+BLOCKER\s+[0-9]+\b`),
	regexp.MustCompile(`(?i)\bthe same blocker\b`),
	regexp.MustCompile(`(?i)\bblocker fix\b`),
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

func TestTriggerConsumerReviewLabelPatternsAllowRuntimeTerms(t *testing.T) {
	for _, text := range []string{
		"The review pipeline reports its result.",
		"A channel send is a blocker until a receiver arrives.",
		"The fixture uses an S3 backend.",
	} {
		for _, pattern := range triggerConsumerReviewLabels {
			if pattern.MatchString(text) {
				t.Errorf("pattern %q rejects durable runtime wording %q", pattern, text)
			}
		}
	}
}

func TestTriggerConsumerReviewLabelPatternsRejectFindingNames(t *testing.T) {
	for _, text := range []string{
		"Regression tests lifted from adversarial review.",
		"The review ran one run id under two pids.",
		"TestSweeper_LeavesALiveRunningDispatchAlone is BLOCKER 1.",
		"The in-process half of the same blocker.",
		"The blocker fix must not recover nothing.",
		"TestHeartbeat_SurvivesATransientStoreError is S1.",
	} {
		matched := false
		for _, pattern := range triggerConsumerReviewLabels {
			matched = matched || pattern.MatchString(text)
		}
		if !matched {
			t.Errorf("review-only wording was accepted: %q", text)
		}
	}
}

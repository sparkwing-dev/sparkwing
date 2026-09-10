package main

import (
	"strings"
	"testing"
)

// The help for both paths promises one listing. Both run against a home with
// no daemon, which exercises the real dispatch without needing one.
func TestQueueListPrintsTheSameListingAsBareQueue(t *testing.T) {
	home := t.TempDir()
	for _, format := range []string{"pretty", "json", "plain"} {
		t.Run(format, func(t *testing.T) {
			bare := captureStdout(t, func() {
				if err := runQueue([]string{"--home", home, "-o", format}); err != nil {
					t.Fatalf("queue: %v", err)
				}
			})
			listed := captureStdout(t, func() {
				if err := runQueue([]string{"list", "--home", home, "-o", format}); err != nil {
					t.Fatalf("queue list: %v", err)
				}
			})
			if bare != listed {
				t.Fatalf("queue and queue list disagree in %s:\n%q\n%q", format, bare, listed)
			}
			if strings.TrimSpace(bare) == "" {
				t.Fatalf("%s listing printed nothing", format)
			}
		})
	}
}

func TestQueueListCarriesItsOwnHelpAndErrors(t *testing.T) {
	if cmdQueueList.Path != "sparkwing queue list" {
		t.Fatalf("queue list path = %q", cmdQueueList.Path)
	}
	err := runQueue([]string{"list", "bogus"})
	if err == nil || !strings.Contains(err.Error(), "sparkwing queue list:") {
		t.Fatalf("queue list error must name the invoked path, got %v", err)
	}
	if err := runQueue([]string{"bogus"}); err == nil || !strings.Contains(err.Error(), "sparkwing queue:") {
		t.Fatalf("bare queue error must name the bare path, got %v", err)
	}
}

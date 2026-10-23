package limits

import (
	"testing"
	"time"
)

func TestTableHasTypedUniqueDocumentedBounds(t *testing.T) {
	seen := map[string]bool{}
	for _, row := range table() {
		if row.Name == "" || seen[row.Name] || row.Doc == "" || row.Refusal == "" {
			t.Errorf("incomplete or duplicate limit: %+v", row)
		}
		seen[row.Name] = true
		switch v := row.Value.(type) {
		case time.Duration:
			if v < 0 {
				t.Errorf("negative duration: %s", row.Name)
			}
		case int64:
			if v < 0 {
				t.Errorf("negative byte bound: %s", row.Name)
			}
		case int:
			if v < 0 {
				t.Errorf("negative count: %s", row.Name)
			}
		default:
			t.Errorf("untyped bound %s: %T", row.Name, v)
		}
	}
	if jobSlack >= RunDeadline || settleMargin >= RunDeadline {
		t.Fatal("remote default leaves no execution budget")
	}
	if MemoMaxTTL > outputRetention {
		t.Fatal("memo may outlive unpinned output")
	}
	if localControllerDrain >= localControllerTermGrace {
		t.Fatal("controller may be killed before drain completes")
	}
	if approvalWait != 0 || totalWallTime != 0 {
		t.Fatal("introduced an unapproved default wait or wall-time cap")
	}
}

func TestTableDocumentsStorageEnforcement(t *testing.T) {
	want := map[string]string{
		"JobScratchBytes":       "Account for fixed Job storage; evict the Job when exhausted.",
		"LocalBinaryCacheBytes": "Prune oldest eligible entries.",
		"MemoMaxTTL":            "Refuse author values above the bound.",
	}
	for _, row := range table() {
		if behavior, ok := want[row.Name]; ok {
			if row.Refusal != behavior {
				t.Errorf("%s behavior = %q, want %q", row.Name, row.Refusal, behavior)
			}
			delete(want, row.Name)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing documented storage limits: %v", want)
	}
}

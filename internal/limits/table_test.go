package limits

import "testing"

func TestExecutionBudgetsAndRetentionRemainConsistent(t *testing.T) {
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

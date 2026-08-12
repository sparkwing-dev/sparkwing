package main

import (
	"os"
	"strings"
	"testing"
)

func TestWindowsReplacementRetainsRecoverableRunningImage(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("update_replace_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	if !strings.Contains(source, "replaceWindowsRunningImage") {
		t.Fatal("Windows replacement has no running-image transaction")
	}
	if strings.Contains(source, "MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH") {
		t.Fatal("Windows replacement still targets the executing image directly")
	}
}

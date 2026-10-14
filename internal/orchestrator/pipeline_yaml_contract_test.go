package orchestrator

import (
	"reflect"
	"testing"
)

func TestOptionsDoesNotCarryUnusedSparkwingDirectory(t *testing.T) {
	if _, ok := reflect.TypeOf(Options{}).FieldByName("SparkwingDir"); ok {
		t.Fatal("Options retains the unused SparkwingDir field")
	}
}

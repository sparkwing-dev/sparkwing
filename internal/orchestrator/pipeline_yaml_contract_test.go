package orchestrator

import (
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

var _ func(string) *pipelines.Pipeline = loadPipelineYAML

func TestOptionsDoesNotCarryUnusedSparkwingDirectory(t *testing.T) {
	if _, ok := reflect.TypeOf(Options{}).FieldByName("SparkwingDir"); ok {
		t.Fatal("Options retains the unused SparkwingDir field")
	}
}

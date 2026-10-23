package sparkwingruntime

import (
	"reflect"
	"testing"
)

func TestSortedUniqueRisks_Dedupes(t *testing.T) {
	got := sortedUniqueRisks(
		[]string{"prod", "destructive"},
		[]string{"prod", "money"},
		[]string{"", "destructive"},
	)
	want := []string{"destructive", "money", "prod"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SortedUniqueRisks = %v, want %v", got, want)
	}
}

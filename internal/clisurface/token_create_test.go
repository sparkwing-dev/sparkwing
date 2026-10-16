package clisurface

import "testing"

func TestTokenCreateRejectsRunnerType(t *testing.T) {
	if _, err := Parse("sparkwing", []string{"tokens", "create", "--type=runner"}); err == nil {
		t.Fatal("runner credential creation must use enrollment")
	}
	if _, err := Parse("sparkwing", []string{"tokens", "list", "--type=runner"}); err != nil {
		t.Fatal(err)
	}
}

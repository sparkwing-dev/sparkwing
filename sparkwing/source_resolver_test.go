package sparkwing_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestNewSecretResolverFromSpecReportsMissesAsErrSecretMissing(t *testing.T) {
	t.Setenv("SW_PRESENT", "value")
	r, err := sparkwing.NewSecretResolverFromSpec(context.Background(), backends.Spec{Type: backends.TypeEnv, Prefix: "SW_"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if v, masked, err := r.Resolve(context.Background(), "PRESENT"); err != nil || v != "value" || !masked {
		t.Fatalf("Resolve = %q masked=%v err=%v", v, masked, err)
	}
	if _, _, err := r.Resolve(context.Background(), "ABSENT"); !errors.Is(err, sparkwing.ErrSecretMissing) {
		t.Fatalf("miss = %v, want ErrSecretMissing", err)
	}
	if _, err := sparkwing.NewSecretResolverFromSpec(context.Background(), backends.Spec{}); err == nil {
		t.Fatal("a spec with no type was accepted")
	}
}

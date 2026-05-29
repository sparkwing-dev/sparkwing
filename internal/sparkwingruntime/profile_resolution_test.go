package sparkwingruntime_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestWithProfileResolution_InstallsRoundTrip(t *testing.T) {
	pr := sparkwing.ProfileResolutionContext{
		Defaults: map[string]string{"target": "prod", "version": "0.6.1"},
		Name:     "prod",
		IsLocal:  false,
	}
	ctx := sparkwingruntime.WithProfileResolution(context.Background(), pr)

	got, ok := ctx.Value(sparkwing.RuntimePlumbing.Keys.ProfileResolution).(sparkwing.ProfileResolutionContext)
	if !ok {
		t.Fatal("install did not register the ProfileResolutionContext under the runtime key")
	}
	if got.Name != "prod" || got.IsLocal != false || got.Defaults["target"] != "prod" {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, pr)
	}
}

func TestWithProfileResolution_ZeroIsNoop(t *testing.T) {
	parent := context.Background()
	got := sparkwingruntime.WithProfileResolution(parent, sparkwing.ProfileResolutionContext{})
	if got != parent {
		t.Error("zero-value install should return the parent ctx unchanged so downstream reads see absence")
	}
}

func TestWithProfileResolution_LocalProfileInstalls(t *testing.T) {
	pr := sparkwing.ProfileResolutionContext{Name: "local", IsLocal: true}
	ctx := sparkwingruntime.WithProfileResolution(context.Background(), pr)

	got, ok := ctx.Value(sparkwing.RuntimePlumbing.Keys.ProfileResolution).(sparkwing.ProfileResolutionContext)
	if !ok || got.Name != "local" || !got.IsLocal {
		t.Errorf("local-only profile install lost: got %+v, ok=%v", got, ok)
	}
}

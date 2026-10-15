package sparkwingtest_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func TestFakeHostAttributesConcurrentStepsAndEval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := sparkwingtest.New(sparkwingtest.Options{})
		defer host.Close()
		plan := sparkwing.NewPlan()
		release := make(chan struct{})
		for _, id := range []string{"a", "b"} {
			sparkwing.Job(plan, id, func(ctx context.Context) error {
				<-release
				sparkwing.Info(ctx, "%s", id)
				return nil
			}).BeforeRun(func(ctx context.Context) error {
				sparkwing.Info(ctx, "eval")
				return nil
			})
		}
		done := make(chan error, 2)
		for _, id := range []string{"a", "b"} {
			go func() { _, err := host.RunStep(t.Context(), plan, id, "run"); done <- err }()
		}
		synctest.Wait()
		close(release)
		for range 2 {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		if _, err := host.Eval(t.Context(), plan, "a", "a/before/0", json.RawMessage(`null`)); err != nil {
			t.Fatal(err)
		}
		records := host.Records()
		if len(records) != 3 {
			t.Fatalf("records = %+v", records)
		}
		for _, record := range records[:2] {
			if record.JobID != record.Msg || record.Step != "run" {
				t.Errorf("step lost its host attribution: %+v", record)
			}
		}
		if records[2].JobID != "" || records[2].Msg != "eval" {
			t.Errorf("eval must write to run logs: %+v", records[2])
		}
	})
}

func TestFakeHostCancelledServicesDoNotInvokeCallbacks(t *testing.T) {
	host := sparkwingtest.New(sparkwingtest.Options{
		OIDCToken: func(context.Context, string) (string, error) {
			t.Error("called OIDC after cancellation")
			return "", nil
		},
		Latest: func(context.Context, string, string) (json.RawMessage, error) {
			t.Error("called Latest after cancellation")
			return nil, nil
		},
		RunPipeline: func(context.Context, string, map[string]string) (json.RawMessage, error) {
			t.Error("called RunPipeline after cancellation")
			return nil, nil
		},
	})
	defer host.Close()
	ctx, cancel := context.WithCancel(host.Context(t.Context()))
	cancel()
	_, err := sparkwing.OIDCToken(ctx, "audience")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("OIDC error = %v", err)
	}
	_, err = sparkwing.RunAndAwait[any, sparkwing.NoInputs](ctx, "pipeline", "job")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("child error = %v", err)
	}
	_, err = sdkserve.ServicesFrom(ctx).Latest(ctx, "pipeline", "job", 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("latest error = %v", err)
	}
}

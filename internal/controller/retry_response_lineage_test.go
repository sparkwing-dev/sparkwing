package controller

import (
	"fmt"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestRetryResponseLineageMatchesFreshReadAndReplay(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(fmt.Sprint(accepted), func(t *testing.T) {
			fixture, options, _, original := retryActionFixture(t, "private-retry-lineage-value", accepted)
			server := httptest.NewServer(fixture.Handler())
			defer server.Close()
			options.URL, options.HTTP = server.URL, server.Client()
			control, err := client.NewEngineClient(options)
			if err != nil {
				t.Fatal(err)
			}
			parent := original.ID
			for generation := range 2 {
				input := controllerwire.RetryRequest{Mode: controllerwire.RetryAll, IdempotencyKey: fmt.Sprintf("lineage-%d", generation), Reason: "retry lineage proof"}
				response, err := control.Retry(t.Context(), parent, input)
				if err != nil {
					t.Fatal(err)
				}
				fresh, err := control.ReadRun(t.Context(), response.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if fresh.Inputs.Lineage.RetryOf != parent || !fresh.Inputs.Lineage.Retrier.Set || fresh.Inputs.Lineage.Retrier.Value.ID != "member" {
					t.Fatalf("stored lineage: %+v", fresh.Inputs.Lineage)
				}
				if !reflect.DeepEqual(response.Inputs.Lineage, fresh.Inputs.Lineage) {
					t.Errorf("retry response lineage %+v differs from fresh read %+v", response.Inputs.Lineage, fresh.Inputs.Lineage)
				}
				if response.Inputs.Actor.ID != "original-author" || response.Inputs.RequestedArgs["secret"] != "***" {
					t.Fatalf("retry response changed source identity or redaction: %+v", response.Inputs)
				}
				replay, err := control.Retry(t.Context(), parent, input)
				if err != nil || !replay.Replayed || replay.RunID != response.RunID {
					t.Fatalf("replay: %+v %v", replay, err)
				}
				if !reflect.DeepEqual(replay.Inputs.Lineage, fresh.Inputs.Lineage) {
					t.Errorf("replayed response lineage %+v differs from fresh read %+v", replay.Inputs.Lineage, fresh.Inputs.Lineage)
				}
				parent = response.RunID
			}
		})
	}
}

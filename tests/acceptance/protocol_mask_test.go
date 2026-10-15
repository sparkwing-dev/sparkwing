package acceptance_test

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

type maskAcknowledgement struct {
	sdkserve.Services
	entered chan struct{}
	ack     chan struct{}
}

func (s *maskAcknowledgement) Mask(ctx context.Context, value string) error {
	close(s.entered)
	select {
	case <-s.ack:
		return s.Services.Mask(ctx, value)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func testGoSDKMaskAcknowledgement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := sparkwingtest.New(sparkwingtest.Options{})
		defer host.Close()
		service := &maskAcknowledgement{entered: make(chan struct{}), ack: make(chan struct{})}
		plan := sparkwing.NewPlan()
		sparkwing.Job(plan, "probe", func(ctx context.Context) error {
			service.Services = sdkserve.ServicesFrom(ctx)
			ctx = sdkserve.WithServices(ctx, service)
			if err := sparkwing.Mask(ctx, "ack-sentinel"); err != nil {
				return err
			}
			sparkwing.Info(ctx, "ack-sentinel")
			sparkwing.Annotate(ctx, sparkwing.Annotation{Message: "ack-sentinel", File: "main.go", Line: 3})
			sparkwing.Summary(ctx, "ack-sentinel")
			return nil
		})
		done := make(chan error, 1)
		go func() {
			_, err := host.RunStep(t.Context(), plan, "probe", "run")
			done <- err
		}()
		<-service.entered
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("Mask returned before acknowledgement: %v", err)
		default:
		}
		if len(host.Records()) != 0 {
			t.Fatal("output escaped the acknowledgement barrier")
		}
		close(service.ack)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		records := host.Records()
		if len(records) != 3 {
			t.Fatalf("records = %+v", records)
		}
		for _, record := range records {
			if record.Msg != "***" {
				t.Errorf("unmasked record: %+v", record)
			}
		}
		if records[1].Event != "annotation" || records[1].Attrs["file"] != "main.go" || records[1].Attrs["line"] != float64(3) || records[2].Event != "summary" {
			t.Fatalf("annotation and summary = %+v", records)
		}
	})
}

package acceptance_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func testGoSDKFakeHostAttributeMasks(t *testing.T) {
	for _, test := range []struct {
		name, secret, attrs, want string
	}{
		{"keys", "attribute-sentinel", `{"attribute-sentinel":"visible","nested":{"prefix-attribute-sentinel":true}}`, `{"***":"visible","nested":{"prefix-***":true}}`},
		{"numbers", "1234567", `{"pin":1234567,"nested":[12345678,{"pin":1234567}]}`, `{"pin":"***","nested":["***8",{"pin":"***"}]}`},
		{"large-numbers", "9007199254740993", `{"pin":9007199254740993}`, `{"pin":"***"}`},
		{"escaped-values", "attribute\"sentinel\nvalue", `{"nested":[{"value":"attribute\"sentinel\nvalue"}]}`, `{"nested":[{"value":"***"}]}`},
		{"unmasked-number", "attribute-sentinel", `{"number":9007199254740993,"count":3}`, `{"number":9007199254740993,"count":3}`},
		{"boolean", "true", `{"flag":true,"other":false}`, `{"flag":"***","other":false}`},
		{"null", "null", `{"empty":null}`, `{"empty":"***"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := sparkwingtest.New(sparkwingtest.Options{Secrets: map[string]sparkwingtest.Secret{
				"TOKEN": {Value: test.secret, Masked: true},
			}})
			t.Cleanup(func() { _ = host.Close() })
			sdkserve.ServicesFrom(host.Context(t.Context())).Emit(nodeproto.FrameLog{
				Message: "visible", Attrs: json.RawMessage(test.attrs),
			})
			records := host.Records()
			if len(records) != 1 || records[0].Msg != "visible" {
				t.Fatalf("fixture did not retain its control record: %+v", records)
			}
			raw, err := json.Marshal(records[0].Attrs)
			if err != nil {
				t.Fatal(err)
			}
			var want map[string]any
			decoder := json.NewDecoder(strings.NewReader(test.want))
			decoder.UseNumber()
			if err := decoder.Decode(&want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(records[0].Attrs, want) {
				t.Fatalf("masked attributes = %s, want %s", raw, test.want)
			}
		})
	}
	t.Run("overlapping-registrations", func(t *testing.T) {
		host := sparkwingtest.New(sparkwingtest.Options{})
		t.Cleanup(func() { _ = host.Close() })
		ctx := host.Context(t.Context())
		for _, value := range []string{"prefix", "prefix-tail"} {
			if err := sparkwing.Mask(ctx, value); err != nil {
				t.Fatal(err)
			}
		}
		sparkwing.Info(ctx, "prefix-tail")
		records := host.Records()
		if len(records) != 1 || records[0].Msg != "***" {
			t.Fatalf("overlapping masks exposed part of a registered value: %+v", records)
		}
	})
}

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
		if records[1].Event != "annotation" || records[1].Attrs["file"] != "main.go" || records[1].Attrs["line"] != json.Number("3") || records[2].Event != "summary" {
			t.Fatalf("annotation and summary = %+v", records)
		}
	})
}

package nodehost_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/sdkserve"
)

type routerSDK struct {
	body func(context.Context, sdkserve.Services) error
}

func (f routerSDK) Context(ctx context.Context, _ nodeproto.RunContext, _ nodeproto.RunnerIdentity, services sdkserve.Services) context.Context {
	return sdkserve.WithServices(ctx, services)
}

func (routerSDK) Document(any) nodeproto.PlanDoc {
	return nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "deploy", Steps: []nodeproto.StepSpec{{ID: "body"}}}}}
}

func (routerSDK) Describe(context.Context) (nodeproto.DescribeDoc, error) {
	return nodeproto.DescribeDoc{}, nil
}

func (f routerSDK) Plan(context.Context, string, map[string]string, nodeproto.RunContext) (any, nodeproto.PlanDoc, error) {
	return struct{}{}, f.Document(nil), nil
}

func (f routerSDK) RunStep(ctx context.Context, _ any, _ nodeproto.Request) (nodeproto.RunResult, error) {
	return nodeproto.RunResult{Outcome: "success"}, f.body(ctx, sdkserve.ServicesFrom(ctx))
}

func (routerSDK) Eval(context.Context, any, nodeproto.Request) (nodeproto.EvalResult, error) {
	return nodeproto.EvalResult{}, errors.New("unexpected evaluation")
}

func TestSDKHTTPServicesUseTheRegisteredProductionRouter(t *testing.T) {
	s := start(t)
	masker := secrets.NewMasker()
	service := newService(t)
	service.Masks = nodehost.NewMaskRegistry(masker)
	service.Secrets = []string{"TOKEN"}
	service.Secret = func(context.Context, capability.Authorized, string) (string, bool, error) {
		return "resolved-credential", true, nil
	}
	service.Output = func(context.Context, capability.Authorized, string) (json.RawMessage, error) {
		return json.RawMessage(`{"image":"ready"}`), nil
	}
	service.Latest = func(context.Context, capability.Authorized, string, string, time.Duration) (nodehost.ProcessOutput, error) {
		return nodehost.ProcessOutput{RunID: "previous", Output: json.RawMessage(`{"image":"ready"}`)}, nil
	}
	service.OIDC = func(context.Context, capability.Authorized, string) (nodehost.ProcessOIDCToken, error) {
		return nodehost.ProcessOIDCToken{Token: "minted-credential", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	children := 0
	service.StartChild = func(context.Context, capability.Authorized, nodehost.ChildRequest) (string, error) {
		children++
		return fmt.Sprintf("child-%d", children), nil
	}
	service.AwaitChild = func(_ context.Context, _ capability.Authorized, run, _ string, _ time.Duration) (nodehost.ProcessChildResult, error) {
		if run == "child-2" {
			return nodehost.ProcessChildResult{RunID: run, Status: "failed", Error: &apierr.Error{Code: apierr.SDKError, Message: "dynamic-credential", Details: map[string]any{"why": "dynamic-credential"}}}, nil
		}
		return nodehost.ProcessChildResult{RunID: run, Status: "success", Output: json.RawMessage(`{"image":"ready"}`)}, nil
	}
	var records []string
	service.Annotation = func(_ context.Context, _ capability.Authorized, request nodeproto.AnnotationRequest) error {
		records = append(records, request.Message)
		return nil
	}
	service.Summary = func(_ context.Context, _ capability.Authorized, request nodeproto.SummaryRequest) error {
		records = append(records, request.Markdown)
		return nil
	}
	bearer := admitService(t, s, service)
	originalTransport := http.DefaultTransport
	http.DefaultTransport = routeTransport{handler: s.handler}
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	complete := make(chan error, 1)
	operations := routerSDK{body: func(ctx context.Context, services sdkserve.Services) error {
		check := func() error {
			value, masked, err := services.Secret(ctx, "TOKEN")
			if err != nil || value != "resolved-credential" || !masked {
				return fmt.Errorf("SDK secret service value=%q masked=%v error=%w", value, masked, err)
			}
			_, _, err = services.Secret(ctx, "OTHER")
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.Forbidden || failure.Details["reason"] != "secret_undeclared" {
				return errors.New("SDK secret refusal lost canonical reason")
			}
			if err := services.Mask(ctx, "dynamic-credential"); err != nil {
				return err
			}
			services.Emit(nodeproto.FrameLog{Event: "annotation", Level: "warn", Message: "dynamic-credential", Step: "body", Attrs: json.RawMessage(`{"file":"main.go","line":3}`)})
			services.Emit(nodeproto.FrameLog{Event: "summary", Message: "dynamic-credential", Step: "body"})
			raw, err := services.Output(ctx, "build")
			if err != nil || string(raw) != `{"image":"ready"}` {
				return errors.New("SDK output service failed")
			}
			raw, err = services.Latest(ctx, "compile", "build", time.Minute)
			if err != nil || string(raw) != `{"image":"ready"}` {
				return errors.New("SDK latest service failed")
			}
			token, err := services.OIDC(ctx, "deploy")
			if err != nil || token != "minted-credential" {
				return errors.New("SDK OIDC service failed")
			}
			raw, err = services.RunPipeline(ctx, sdkserve.ChildRequest{Pipeline: "compile", Job: "build", Args: map[string]string{}})
			if err != nil || string(raw) != `{"image":"ready"}` {
				return errors.New("SDK child service failed")
			}
			_, err = services.RunPipeline(ctx, sdkserve.ChildRequest{Pipeline: "compile", Job: "build", Args: map[string]string{}})
			if !errors.As(err, &failure) || failure.Code != apierr.SDKError || strings.Contains(failure.Error(), "dynamic-credential") {
				return errors.New("SDK child failure did not preserve canonical masked error")
			}
			return nil
		}
		err := check()
		complete <- err
		return err
	}}
	input, writer := io.Pipe()
	var outputFrames bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- sdkserve.Serve(ctx, input, &outputFrames, sdkserve.Config{SDKVersion: "v1", Operations: operations})
	}()
	defer input.Close()
	defer writer.Close()
	hello := map[string]any{"id": "hello", "op": "hello", "protocol": 2, "engine": map[string]string{"version": "v1"}, "runner": map[string]any{"name": "local", "type": "local", "labels": []string{}}, "binding": nodeproto.ProcessBinding{RunID: "run-1", JobID: "deploy", Routes: nodeproto.ProcessRoutes{URL: s.URL(), Bearer: bearer}}}
	go func() {
		encoder := json.NewEncoder(writer)
		if err := encoder.Encode(hello); err != nil {
			return
		}
		if _, err := io.WriteString(writer, `{"id":"plan","op":"plan","pipeline":"release","args":{},"run":{"run_id":"run-1","pipeline":"release"}}`+"\n"+`{"id":"step","op":"run_step","job":"deploy","step":"body","attempt":1,"settled":[]}`+"\n"); err != nil {
			return
		}
	}()
	select {
	case err := <-complete:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("SDK route calls did not finish before deadline")
	}
	_ = writer.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("SDK protocol did not join before deadline")
	}
	if len(records) != 2 || strings.Contains(strings.Join(records, " "), "dynamic-credential") {
		t.Fatalf("records were not acknowledged and masked: %v %+v", records, records)
	}
	for _, credential := range []string{"dynamic-credential", "resolved-credential", "minted-credential"} {
		if masker.Mask(credential) != "***" {
			t.Fatalf("credential %q was not registered before acknowledgement", credential)
		}
	}
}

func (routerSDK) Arguments(any) []nodeproto.ArgumentDescription { return nil }

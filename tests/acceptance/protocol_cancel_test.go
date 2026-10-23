package acceptance_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/pkg/sdkserve"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

type maskAdmissionContext struct {
	context.Context
	once         sync.Once
	checked      chan struct{}
	continueCall chan struct{}
}

func (ctx *maskAdmissionContext) Err() error {
	err := ctx.Context.Err()
	ctx.once.Do(func() {
		close(ctx.checked)
		<-ctx.continueCall
	})
	return err
}

func testGoSDKMaskCannotCommitAfterClose(t *testing.T) {
	recordLeaf(t)
	host := sparkwingtest.New(sparkwingtest.Options{})
	t.Cleanup(func() { _ = host.Close() })
	ctx := &maskAdmissionContext{
		Context: context.WithoutCancel(host.Context(t.Context())),
		checked: make(chan struct{}), continueCall: make(chan struct{}),
	}
	result := make(chan error, 1)
	go func() { result <- sparkwing.Mask(ctx, "racing-mask") }()
	<-ctx.checked
	if err := host.Close(); err != nil {
		t.Error(err)
	}
	close(ctx.continueCall)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Errorf("mask admitted before Close returned %v, want cancellation", err)
	}
	if masks := host.Masks(); len(masks) != 0 {
		t.Fatalf("mask committed after Close: %v", masks)
	}
}

func testGoSDKClosedFakeHostServices(t *testing.T) {
	host := sparkwingtest.New(sparkwingtest.Options{
		Secrets: map[string]sparkwingtest.Secret{
			"TOKEN": {Value: "fixture-secret", Masked: true},
			"PLAIN": {Value: "fixture-variable"},
		},
		Outputs: map[string]json.RawMessage{"producer": json.RawMessage(`{"value":1}`)},
	})
	defer host.Close()
	var ctx context.Context
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "capture", func(body context.Context) error {
		ctx = context.WithoutCancel(body)
		return nil
	})
	if _, err := host.RunStep(t.Context(), plan, "capture", "run"); err != nil {
		t.Fatal(err)
	}
	// safety: dropping request cancellation cannot reopen a closed fixture's services.
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	recordError := func(write func()) (err error) {
		defer func() {
			if value := recover(); value != nil {
				var ok bool
				err, ok = value.(error)
				if !ok {
					t.Fatalf("record write panicked with %v", value)
				}
			}
		}()
		write()
		return nil
	}
	for name, call := range map[string]func() error{
		"secret":   func() error { _, err := sparkwing.Secret(ctx, "TOKEN"); return err },
		"variable": func() error { _, err := sparkwing.Variable(ctx, "PLAIN"); return err },
		"output":   func() error { _, err := sdkserve.ServicesFrom(ctx).Output(ctx, "producer"); return err },
		"mask":     func() error { return sparkwing.Mask(ctx, "after-close") },
		"oidc":     func() error { _, err := sparkwing.OIDCToken(ctx, "audience"); return err },
		"latest":   func() error { _, err := sdkserve.ServicesFrom(ctx).Latest(ctx, "pipeline", "job", 0); return err },
		"child": func() error {
			_, err := sparkwing.RunAndAwait[any, sparkwing.NoInputs](ctx, "pipeline", "job")
			return err
		},
		"annotation": func() error {
			return recordError(func() { sparkwing.Annotate(ctx, sparkwing.Annotation{Message: "after-close"}) })
		},
		"summary": func() error {
			return recordError(func() { sparkwing.Summary(ctx, "after-close") })
		},
	} {
		t.Run(name, func(t *testing.T) {
			recordLeaf(t)
			if err := call(); !errors.Is(err, context.Canceled) {
				t.Fatalf("closed fixture service = %v, want cancellation", err)
			}
		})
	}
	for _, value := range host.Masks() {
		if value == "after-close" {
			t.Fatal("closed fixture acknowledged a mask")
		}
	}
	if records := host.Records(); len(records) != 0 {
		t.Fatalf("closed fixture persisted records: %+v", records)
	}
}

func testGoSDKFakeHostCloseJoinsWork(t *testing.T) {
	for _, tc := range []struct {
		operation string
		detached  bool
	}{
		{operation: "step"},
		{operation: "eval"},
		{operation: "oidc"},
		{operation: "latest"},
		{operation: "child"},
		{operation: "oidc", detached: true},
		{operation: "latest", detached: true},
		{operation: "child", detached: true},
	} {
		name := tc.operation
		if tc.detached {
			name += "-detached"
		}
		t.Run(name, func(t *testing.T) {
			recordLeaf(t)
			synctest.Test(t, func(t *testing.T) {
				started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
				abort := make(chan struct{})
				wait := func(ctx context.Context) error {
					close(started)
					select {
					case <-ctx.Done():
						close(cancelled)
					case <-abort:
					}
					<-release
					return ctx.Err()
				}
				host := sparkwingtest.New(sparkwingtest.Options{
					OIDCToken: func(ctx context.Context, _ string) (string, error) { return "", wait(ctx) },
					Latest:    func(ctx context.Context, _, _ string) (json.RawMessage, error) { return nil, wait(ctx) },
					RunPipeline: func(ctx context.Context, _ string, _ map[string]string) (json.RawMessage, error) {
						return nil, wait(ctx)
					},
				})
				defer host.Close()
				plan := sparkwing.NewPlan()
				sparkwing.Job(plan, "job", wait).BeforeRun(wait)
				finished := make(chan error, 1)
				ctx := host.Context(t.Context())
				if tc.detached {
					ctx = context.WithoutCancel(ctx)
				}
				go func() {
					var err error
					switch tc.operation {
					case "step":
						_, err = host.RunStep(t.Context(), plan, "job", "run")
					case "eval":
						_, err = host.Eval(t.Context(), plan, "job", "job/before/0", json.RawMessage(`null`))
					case "oidc":
						_, err = sparkwing.OIDCToken(ctx, "audience")
					case "latest":
						_, err = sdkserve.ServicesFrom(ctx).Latest(ctx, "pipeline", "job", 0)
					case "child":
						_, err = sparkwing.RunAndAwait[any, sparkwing.NoInputs](ctx, "pipeline", "job")
					}
					finished <- err
				}()
				<-started
				closed := make(chan error, 1)
				go func() { closed <- host.Close() }()
				synctest.Wait()
				select {
				case <-cancelled:
				default:
					t.Error("Close did not cancel active work")
				}
				select {
				case <-closed:
					t.Error("Close returned before active work finished")
				default:
				}
				close(abort)
				close(release)
				if err := <-finished; err == nil {
					t.Error("cancelled work returned success")
				}
				synctest.Wait()
				select {
				case err := <-closed:
					if err != nil {
						t.Error(err)
					}
				default:
					t.Error("Close did not join completed work")
				}
				if _, err := host.RunStep(t.Context(), plan, "job", "run"); !errors.Is(err, context.Canceled) {
					t.Errorf("step after Close: %v", err)
				}
			})
		})
	}
}

func testGoSDKQueuedEvalCancellation(t *testing.T) {
	recordLeaf(t)
	synctest.Test(t, func(t *testing.T) {
		host := sparkwingtest.New(sparkwingtest.Options{})
		defer host.Close()
		plan := sparkwing.NewPlan()
		started, release := make(chan struct{}), make(chan struct{})
		invoked := false
		sparkwing.Job(plan, "job", func(context.Context) error { return nil }).
			BeforeRun(func(context.Context) error {
				close(started)
				<-release
				return nil
			}).BeforeRun(func(context.Context) error {
			invoked = true
			return nil
		})
		closures := sdkserve.DefaultOperations.Document(plan).Jobs[0].Closures.BeforeRun
		first, second := make(chan error, 1), make(chan error, 1)
		go func() {
			_, err := host.Eval(t.Context(), plan, "job", closures[0], nil)
			first <- err
		}()
		<-started
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() {
			_, err := host.Eval(ctx, plan, "job", closures[1], nil)
			second <- err
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
		select {
		case err := <-second:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("queued eval cancellation = %v", err)
			}
		default:
			t.Error("cancelled eval remained blocked on unrelated work")
		}
		close(release)
		if err := <-first; err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if invoked {
			t.Fatal("cancelled queued closure executed")
		}
		if _, err := host.Eval(t.Context(), plan, "job", closures[1], nil); err != nil || !invoked {
			t.Fatalf("eval after cancellation = %v, invoked = %t", err, invoked)
		}
	})
}

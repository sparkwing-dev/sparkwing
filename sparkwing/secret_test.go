package sparkwing

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSecret_NoResolverInstalled(t *testing.T) {
	_, err := Secret(context.Background(), "FOO")
	if err == nil {
		t.Fatal("Secret without resolver in ctx must error")
	}
	if !strings.Contains(err.Error(), "no resolver installed") {
		t.Fatalf("err = %v, want one mentioning the missing resolver", err)
	}
}

func TestSecret_BlankName(t *testing.T) {
	ctx := withSecretResolver(context.Background(), secretResolverFunc(
		func(ctx context.Context, name string) (string, bool, error) { return "x", true, nil },
	))
	if _, err := Secret(ctx, ""); err == nil {
		t.Fatal("Secret(ctx, \"\") must error")
	}
}

func TestSecret_DelegatesToResolver(t *testing.T) {
	called := 0
	ctx := withSecretResolver(context.Background(), secretResolverFunc(
		func(ctx context.Context, name string) (string, bool, error) {
			called++
			if name != "TOKEN" {
				t.Errorf("resolver got %q, want TOKEN", name)
			}
			return "abc123", true, nil
		},
	))
	got, err := Secret(ctx, "TOKEN")
	if err != nil {
		t.Fatalf("Secret: %v", err)
	}
	if got != "abc123" {
		t.Fatalf("Secret = %q, want abc123", got)
	}
	if called != 1 {
		t.Fatalf("resolver called %d times, want 1", called)
	}
}

func TestSecret_PropagatesResolverError(t *testing.T) {
	want := errors.New("source unreachable")
	ctx := withSecretResolver(context.Background(), secretResolverFunc(
		func(ctx context.Context, name string) (string, bool, error) { return "", false, want },
	))
	_, err := Secret(ctx, "FOO")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want unwrap to %v", err, want)
	}
	if !strings.Contains(err.Error(), `"FOO"`) {
		t.Fatalf("err = %v, want it to mention the secret name", err)
	}
}

func TestSecret_RejectsUnmaskedEntry(t *testing.T) {
	ctx := withSecretResolver(context.Background(), secretResolverFunc(
		func(ctx context.Context, name string) (string, bool, error) {
			return "us-east-1", false, nil
		},
	))
	_, err := Secret(ctx, "REGION")
	if err == nil {
		t.Fatal("expected error for masked=false entry")
	}
	if !strings.Contains(err.Error(), "use sparkwing.Variable") {
		t.Fatalf("err = %v, want hint to use Variable", err)
	}
}

func TestMustSecret_PanicMessageIncludesName(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		msg := ""
		switch v := r.(type) {
		case error:
			msg = v.Error()
		case string:
			msg = v
		default:
			t.Fatalf("panic of unexpected type %T: %v", r, r)
		}
		if !strings.Contains(msg, `"DATABASE_URL"`) {
			t.Fatalf("panic %q must include the secret name", msg)
		}
		if !strings.Contains(msg, "MustSecret") {
			t.Fatalf("panic %q should name the calling helper", msg)
		}
	}()
	MustSecret(context.Background(), "DATABASE_URL")
}

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine/sdkinstall"
)

func TestSDKInstallPassesRequestToEngineOwner(t *testing.T) {
	input := strings.NewReader(`{"directory":"/checkout","pipeline_dir":".sparkwing","sdk":"go"}`)
	var output, diagnostics bytes.Buffer
	want := errors.New("installation failed")
	called := false
	err := runSDKInstallCommand(t.Context(), nil, "v1.2.3", input, &output, &diagnostics, func(ctx context.Context, version string, request io.Reader, out, diagnostic io.Writer) error {
		called = true
		if ctx != t.Context() || version != "v1.2.3" || request != input || out != &output || diagnostic != &diagnostics {
			t.Fatal("installer boundaries changed")
		}
		return want
	})
	if !called || !errors.Is(err, want) {
		t.Fatalf("called=%v error=%v", called, err)
	}
}

func TestSDKInstallRejectsArgumentsBeforeEngineOwner(t *testing.T) {
	for _, args := range [][]string{{"argument"}, {"--unknown"}, {"--home", "/checkout"}} {
		err := runSDKInstallCommand(t.Context(), args, "version", nil, io.Discard, io.Discard, nil)
		if err == nil {
			t.Fatalf("accepted arguments %q", args)
		}
	}
	var help bytes.Buffer
	if err := runSDKInstallCommand(t.Context(), []string{"--help"}, "version", nil, &help, nil, nil); err != nil || help.Len() == 0 {
		t.Fatalf("help=%q error=%v", help.String(), err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := runSDKInstallCommand(ctx, nil, "version", nil, io.Discard, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled install=%v", err)
	}
}

func TestSDKInstallRejectsInvalidRequestsThroughEngineOwner(t *testing.T) {
	for _, body := range []string{"", "null", "{}", `{"directory":"/checkout","directory":"/other"}`, `{"environment":["TOKEN=secret"]}`, `{} {}`, `{"directory":"relative","pipeline_dir":".","sdk":"go"}`, `{"directory":"/checkout","pipeline_dir":"../outside","sdk":"go"}`} {
		var out bytes.Buffer
		err := runSDKInstallCommand(t.Context(), nil, "version", strings.NewReader(body), &out, io.Discard, sdkinstall.Run)
		if err == nil || out.Len() != 0 {
			t.Fatalf("request=%q output=%q error=%v", body, out.String(), err)
		}
	}
}

func TestSDKInstallHiddenDispatch(t *testing.T) {
	if err := runSparkwing([]string{"__sdk-install", "unexpected"}); err == nil {
		t.Fatal("accepted unexpected installer argument")
	}
}

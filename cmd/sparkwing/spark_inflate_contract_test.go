package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/sparks"
)

func TestSparksInflateContract(t *testing.T) {
	for _, library := range []string{"templates", "github.com/acme/my-sparks"} {
		for _, format := range []string{"json", "plain", "pretty"} {
			t.Run(library+"/"+format, func(t *testing.T) {
				args := []string{"--library", library, "-o", format}
				if _, err := clisurface.Parse("sparkwing", append([]string{"pipeline", "sparks", "inflate"}, args...)); err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				calls := 0
				err := runSparksInflateCommandArgs(context.Background(), args, &out,
					func() (string, error) { return "/repo/.sparkwing", nil },
					func(ctx context.Context, dir, module string) (*sparks.VendorResult, error) {
						calls++
						if dir != "/repo/.sparkwing" || module != resolveVendorModulePath(library) || ctx.Err() != nil {
							t.Fatalf("vendor inputs: %s %s %v", dir, module, ctx.Err())
						}
						return &sparks.VendorResult{ModulePath: module, Version: "v1.2.3", Dest: "/repo/.sparkwing/sparks/lib", RelReplace: "./sparks/lib"}, nil
					})
				if err != nil || calls != 1 {
					t.Fatalf("inflate: %v, calls=%d", err, calls)
				}
				switch format {
				case "json":
					var receipt map[string]string
					if err := json.Unmarshal(out.Bytes(), &receipt); err != nil || receipt["module"] != resolveVendorModulePath(library) || receipt["dest"] != "/repo/.sparkwing/sparks/lib" {
						t.Fatalf("receipt: %s, %v", &out, err)
					}
				case "plain":
					if out.String() != "/repo/.sparkwing/sparks/lib\n" {
						t.Fatalf("plain: %q", &out)
					}
				case "pretty":
					if !strings.Contains(out.String(), "vendored "+resolveVendorModulePath(library)+"@v1.2.3") {
						t.Fatal(out.String())
					}
				}
			})
		}
	}
}

func TestSparksInflateRefusesInvalidInputBeforeLookup(t *testing.T) {
	for _, args := range [][]string{nil, {"--module", "templates"}, {"templates"}, {"--library="}, {"--library", "templates", "-o", "xml"}} {
		var out bytes.Buffer
		err := runSparksInflateCommandArgs(context.Background(), args, &out,
			func() (string, error) { t.Fatal("looked up project for invalid input"); return "", nil }, nil)
		if err == nil || out.Len() != 0 {
			t.Fatalf("args=%v: %v, %s", args, err, &out)
		}
	}
}

func TestSparksInflateHelpAndCancellation(t *testing.T) {
	var out bytes.Buffer
	lookup := func() (string, error) { t.Fatal("unexpected project lookup"); return "", nil }
	if err := runSparksInflateCommandArgs(context.Background(), []string{"--help"}, &out, lookup, nil); err != nil || !strings.Contains(out.String(), "--library") {
		t.Fatalf("help: %s %v", &out, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out.Reset()
	if err := runSparksInflateCommandArgs(ctx, []string{"--library", "templates"}, &out, lookup, nil); !errors.Is(err, context.Canceled) || out.Len() != 0 {
		t.Fatalf("cancel: %s %v", &out, err)
	}
}

func TestSparksInflateErrorsRemainFailures(t *testing.T) {
	failure := errors.New("vendor failed")
	err := runSparksInflateCommandArgs(context.Background(), []string{"--library", "templates"}, io.Discard,
		func() (string, error) { return "/repo/.sparkwing", nil },
		func(context.Context, string, string) (*sparks.VendorResult, error) { return nil, failure })
	if !errors.Is(err, failure) {
		t.Fatalf("vendor error: %v", err)
	}
	err = runSparksInflateCommandArgs(context.Background(), []string{"--library", "templates"}, retryBrokenWriter{},
		func() (string, error) { return "/repo/.sparkwing", nil },
		func(context.Context, string, string) (*sparks.VendorResult, error) {
			return &sparks.VendorResult{}, nil
		})
	if err == nil {
		t.Fatal("lost output error")
	}
}

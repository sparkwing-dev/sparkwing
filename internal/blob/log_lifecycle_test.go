package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func TestLifecycleRuleMustExpireTheClassPrefixAfterItsDays(t *testing.T) {
	rule := func(prefix string, days int32, enabled bool) types.LifecycleRule {
		r := types.LifecycleRule{
			Filter:     &types.LifecycleRuleFilter{Prefix: aws.String(prefix)},
			Expiration: &types.LifecycleExpiration{Days: aws.Int32(days)},
			Status:     types.ExpirationStatusDisabled,
		}
		if enabled {
			r.Status = types.ExpirationStatusEnabled
		}
		return r
	}
	const prefix = "logs/r90/"
	for name, tc := range map[string]struct {
		rules []types.LifecycleRule
		want  bool
	}{
		"exact rule":     {[]types.LifecycleRule{rule(prefix, 90, true)}, true},
		"broader rule":   {[]types.LifecycleRule{rule("logs/", 90, true)}, true},
		"wrong days":     {[]types.LifecycleRule{rule(prefix, 30, true)}, false},
		"disabled":       {[]types.LifecycleRule{rule(prefix, 90, false)}, false},
		"another prefix": {[]types.LifecycleRule{rule("logs/r30/", 90, true)}, false},
		"no rules":       {nil, false},
		"tag filter":     {[]types.LifecycleRule{{Status: types.ExpirationStatusEnabled, Expiration: &types.LifecycleExpiration{Days: aws.Int32(90)}, Filter: &types.LifecycleRuleFilter{Tag: &types.Tag{Key: aws.String("k"), Value: aws.String("v")}}}}, false},
		"size filter":    {[]types.LifecycleRule{{Status: types.ExpirationStatusEnabled, Expiration: &types.LifecycleExpiration{Days: aws.Int32(90)}, Filter: &types.LifecycleRuleFilter{Prefix: aws.String(prefix), ObjectSizeGreaterThan: aws.Int64(1)}}}, false},
		"and filter":     {[]types.LifecycleRule{{Status: types.ExpirationStatusEnabled, Expiration: &types.LifecycleExpiration{Days: aws.Int32(90)}, Filter: &types.LifecycleRuleFilter{And: &types.LifecycleRuleAndOperator{Prefix: aws.String(prefix)}}}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := logLifecycleExpires(tc.rules, prefix, 90); got != tc.want {
				t.Fatalf("lifecycleExpires = %v, want %v", got, tc.want)
			}
		})
	}
	if logLifecycleExpires([]types.LifecycleRule{rule(prefix, 90, true)}, prefix, 0) {
		t.Fatal("zero-day class appeared configured")
	}
	if largeDays := uint64(1)<<32 + 90; uint64(int(^uint(0)>>1)) >= largeDays {
		if logLifecycleExpires([]types.LifecycleRule{rule(prefix, 90, true)}, prefix, int(largeDays)) {
			t.Fatal("overflowed class matched a 90-day rule")
		}
	}
}

func TestS3LogLifecycleReadiness(t *testing.T) {
	for _, class := range []string{"r30", "r90", "r120"} {
		for _, response := range []string{"configured", "absent", "wrong days", "denied"} {
			t.Run(class+"/"+response, func(t *testing.T) {
				backend := logS3(t, func(r *http.Request) (*http.Response, error) {
					if r.Method != http.MethodGet || r.URL.Path != "/bucket" || !r.URL.Query().Has("lifecycle") {
						t.Fatalf("unexpected lifecycle request: %s %s", r.Method, r.URL.Path)
					}
					days, _ := LogClassDays(class)
					if response == "wrong days" {
						days++
					}
					status := http.StatusOK
					raw := fmt.Sprintf(`<LifecycleConfiguration><Rule><ID>logs</ID><Status>Enabled</Status><Filter><Prefix>logs/%s/</Prefix></Filter><Expiration><Days>%d</Days></Expiration></Rule></LifecycleConfiguration>`, class, days)
					if response == "absent" {
						status, raw = http.StatusNotFound, `<Error><Code>NoSuchLifecycleConfiguration</Code></Error>`
					}
					if response == "denied" {
						status, raw = http.StatusForbidden, `<Error><Code>AccessDenied</Code></Error>`
					}
					return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(raw)), Request: r}, nil
				})
				err := backend.LogClassReady(t.Context(), class)
				if (err == nil) != (response == "configured") {
					t.Fatalf("lifecycle = %v", err)
				}
				if response == "denied" && !strings.Contains(err.Error(), "AccessDenied") {
					t.Fatalf("lost backend error: %v", err)
				}
			})
		}
	}
	backend := logS3(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid lifecycle request reached S3")
		return nil, nil
	})
	if err := backend.LogClassReady(t.Context(), "r+1"); err == nil {
		t.Fatal("invalid class appeared ready")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := backend.LogClassReady(ctx, "r90"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled readiness = %v", err)
	}
}

package apierr_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func TestDecodeInnerRejectsMalformedErrors(t *testing.T) {
	cases := map[string]string{
		"empty":              ``,
		"null":               `null`,
		"array":              `[]`,
		"missing code":       `{"message":"secret-payload"}`,
		"missing message":    `{"code":"internal"}`,
		"empty code":         `{"code":"","message":"secret-payload"}`,
		"null code":          `{"code":null,"message":"secret-payload"}`,
		"numeric code":       `{"code":1,"message":"secret-payload"}`,
		"null message":       `{"code":"internal","message":null}`,
		"numeric message":    `{"code":"internal","message":1}`,
		"null hint":          `{"code":"internal","message":"secret-payload","hint":null}`,
		"numeric hint":       `{"code":"internal","message":"secret-payload","hint":1}`,
		"null details":       `{"code":"internal","message":"secret-payload","details":null}`,
		"array details":      `{"code":"internal","message":"secret-payload","details":[]}`,
		"string details":     `{"code":"internal","message":"secret-payload","details":"secret-payload"}`,
		"envelope":           `{"error":{"code":"internal","message":"secret-payload"}}`,
		"mixed envelope":     `{"code":"internal","message":"secret-payload","error":{}}`,
		"duplicate code":     `{"code":"internal","code":"future","message":"secret-payload"}`,
		"escaped duplicate":  `{"code":"internal","c\u006fde":"future","message":"secret-payload"}`,
		"nested duplicate":   `{"code":"internal","message":"secret-payload","details":{"x":1,"x":2}}`,
		"additive duplicate": `{"code":"internal","message":"secret-payload","extra":[{"x":1,"x":2}]}`,
		"trailing value":     `{"code":"internal","message":"secret-payload"} null`,
		"trailing garbage":   `{"code":"internal","message":"secret-payload"} secret-payload`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			failure, err := apierr.DecodeInner([]byte(data))
			if err == nil || failure != nil {
				t.Fatalf("DecodeInner = (%v, %v), want nil and error", failure, err)
			}
			if strings.Contains(err.Error(), "secret-payload") {
				t.Fatal("decode error exposed payload text")
			}
		})
	}
}

func TestDecodeInnerPreservesFutureCodesAndDetails(t *testing.T) {
	failure, err := apierr.DecodeInner([]byte(` {
		"code":"future_code","message":"safe message","hint":"try later",
		"details":{"reason":"future_reason","count":3,"nested":[{"x":true},{"x":false}]},
		"extra":{"x":1}
	} `))
	if err != nil {
		t.Fatal(err)
	}
	if failure.Code != "future_code" || failure.Message != "safe message" || failure.Hint != "try later" {
		t.Fatalf("unexpected error fields: %#v", failure)
	}
	if failure.Details["reason"] != "future_reason" || failure.Details["count"] != float64(3) {
		t.Fatalf("unexpected details: %#v", failure.Details)
	}
	if _, err := apierr.DecodeInner([]byte(`{"code":"internal","message":""}`)); err != nil {
		t.Fatalf("empty string message: %v", err)
	}
}

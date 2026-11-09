package apierr_test

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestDecodeInner(t *testing.T) {
	for _, body := range []string{
		`null`, `[]`, `{}`, `{"code":"forbidden"}`, `{"code":null,"message":"x"}`,
		`{"code":"","message":"x"}`, `{"code":4,"message":"x"}`, `{"code":"x","message":null}`,
		`{"code":"x","message":"x","hint":null}`, `{"code":"x","message":"x","details":[]}`,
		`{"code":"x","message":"x","details":null}`, `{"error":{"code":"x","message":"x"}}`,
		`{"code":"x","message":"x","error":{}}`, `{"code":"x","code":"y","message":"x"}`,
		`{"code":"x","message":"x","details":{"reason":1,"reason":2}}`,
		`{"code":"x","message":"x"} {}`, `{"code":"x","message":"x"} garbage`,
	} {
		t.Run(body, func(t *testing.T) {
			if _, err := apierr.DecodeInner([]byte(body)); err == nil {
				t.Fatal("malformed inner error accepted")
			}
		})
	}
	got, err := apierr.DecodeInner([]byte(`{"code":"future_code","message":"future error","hint":"retry","details":{"retry_after_ms":1001},"future":{"enabled":true}}`))
	if err != nil || got.Code != "future_code" || got.Hint != "retry" {
		t.Fatalf("additive error: %+v, %v", got, err)
	}
}

func TestErrorUnmarshalUsesInnerCodec(t *testing.T) {
	for _, body := range []string{`{}`, `{"code":"x"}`, `{"code":"x","code":"y","message":"x"}`, `{"error":{"code":"x","message":"x"}}`} {
		var failure apierr.Error
		if err := json.Unmarshal([]byte(body), &failure); err == nil {
			t.Fatalf("unmarshal accepted invalid inner error: %s", body)
		}
	}
	var failure apierr.Error
	if err := json.Unmarshal([]byte(`{"code":"future_code","message":"readable","future":true}`), &failure); err != nil || failure.Code != "future_code" {
		t.Fatalf("additive inner error = %+v, %v", failure, err)
	}
}

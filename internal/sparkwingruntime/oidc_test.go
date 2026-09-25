package sparkwingruntime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestOIDCTokenReadsTheInstalledSource(t *testing.T) {
	ctx := context.Background()
	if _, err := sparkwing.OIDCToken(ctx, "sts.amazonaws.com"); !errors.Is(err, sparkwing.ErrOIDCUnavailable) {
		t.Fatalf("no source: err = %v, want ErrOIDCUnavailable", err)
	}
	var asked string
	ctx = sparkwingruntime.WithOIDCTokenSource(ctx, func(_ context.Context, audience string) (string, error) {
		asked = audience
		return "signed", nil
	})
	tok, err := sparkwing.OIDCToken(ctx, "sts.amazonaws.com")
	if err != nil || tok != "signed" || asked != "sts.amazonaws.com" {
		t.Fatalf("token %q err %v audience %q, want the source's token for the audience asked", tok, err, asked)
	}
}

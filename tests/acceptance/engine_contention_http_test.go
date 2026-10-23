package acceptance_test

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"testing/quick"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func TestEngineTransactionContentionProperty(t *testing.T) {
	property := func(input []byte, state, wrapping uint8) bool {
		secret := "private-database-" + hex.EncodeToString(input)
		var failure error = &pgconn.PgError{Code: []string{"40001", "40P01", "55P03", "XX000"}[state%4], Message: secret, Detail: secret}
		switch wrapping % 3 {
		case 1:
			failure = fmt.Errorf("database: %w", failure)
		case 2:
			failure = errors.Join(errors.New(secret), failure)
		}
		f := &engineTransactions{err: failure}
		w := engineHTTP(t, newCredentialBoundary(), f, "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start"}`)
		status, code, retry := http.StatusServiceUnavailable, apierr.Unavailable, "1"
		if state%4 == 3 {
			status, code, retry = http.StatusInternalServerError, apierr.Internal, ""
		}
		assertEngineError(t, w, status, code)
		return f.writes == 1 && w.Header().Get("Retry-After") == retry && !strings.Contains(w.Body.String(), secret)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 60}); err != nil {
		t.Fatal(err)
	}
}

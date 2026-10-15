package nodehost

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type upstreamOutputTransport struct{ body []byte }

func (u upstreamOutputTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(u.body)), Header: make(http.Header), Request: r}, nil
}

func TestUpstreamOutputRejectsOversizedVerifiedJSON(t *testing.T) {
	for _, size := range []int64{limits.NodeOutputBytes, limits.NodeOutputBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			body := bytes.Repeat([]byte("x"), int(size))
			body[0], body[len(body)-1] = '"', '"'
			sum := sha256.Sum256(body)
			claim := &Claim{base: "https://controller.test", http: &http.Client{Transport: upstreamOutputTransport{body}}}
			value, err := claim.fetchOutput(t.Context(), store.OutputReadGrant{URL: "/blob", SHA256: hex.EncodeToString(sum[:]), Size: size})
			if size > limits.NodeOutputBytes {
				if len(value) != 0 || !errors.Is(err, errUpstream) {
					t.Fatalf("value=%d bytes, err=%v", len(value), err)
				}
			} else if err != nil || !bytes.Equal(value, body) {
				t.Fatalf("value=%d bytes, err=%v", len(value), err)
			}
		})
	}
}

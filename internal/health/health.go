package health

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	StatusOK       = "ok"
	StatusDegraded = "degraded"
)

const MaxBodyBytes = 1 << 16

type Response struct {
	Status   string   `json:"status"`
	Problems []string `json:"problems,omitempty"`

	Auth string `json:"auth,omitempty"`
}

func (r Response) Degraded() bool {
	return r.Status == StatusDegraded || len(r.Problems) > 0
}

var ErrNotContract = errors.New("health body is not the JSON contract")

func Decode(r io.Reader) (Response, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxBodyBytes))
	if err != nil {
		return Response{}, fmt.Errorf("read health body: %w", err)
	}
	var out Response
	if err := json.Unmarshal(raw, &out); err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrNotContract, err)
	}
	return out, nil
}

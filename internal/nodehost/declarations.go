package nodehost

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func value[T any](p *T) (v T) {
	if p != nil {
		return *p
	}
	return v
}

func jobHash(job nodeproto.JobSpec) string {
	raw, err := json.Marshal(job)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

// ConcurrencyKey scopes a validated group using host-owned run and machine identity.
func ConcurrencyKey(c nodeproto.ConcurrencySpec, runID, boxID string) string {
	switch c.Scope {
	case "run":
		return "run:" + runID + ":" + c.Group
	case "box":
		return "box:" + boxID + ":" + c.Group
	default:
		return "global:" + c.Group
	}
}

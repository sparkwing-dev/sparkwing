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
	return scopedConcurrencyKey(c.Group, value(c.Scope), runID, boxID)
}

func scopedConcurrencyKey(name, scope, runID, boxID string) string {
	switch scope {
	case "run":
		return "run:" + runID + ":" + name
	case "box":
		return "box:" + boxID + ":" + name
	default:
		return "global:" + name
	}
}

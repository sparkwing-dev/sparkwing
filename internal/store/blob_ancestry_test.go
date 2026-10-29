package store

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func FuzzBlobAncestryStaysWithinTenantAndSealedDependencies(f *testing.F) {
	if testing.Short() {
		f.Skip("slow: 1.0s measured in the short class; the full suite runs it")
	}
	for _, graph := range []uint64{0, 1, 42, 0x1234, 0x7fff} {
		for kind := uint8(0); kind < 4; kind++ {
			f.Add(graph, kind)
		}
	}
	f.Fuzz(func(t *testing.T, graph uint64, kind uint8) {
		s, tenant, _, _ := resultFixture(t)
		const count = 6
		var reachable [count][count]bool
		var names [count]string
		for i := range names {
			names[i] = fmt.Sprintf("job-%d", i)
		}
		bit := uint(0)
		for i := range names {
			spec := nodeproto.JobSpec{ID: names[i], Groups: []string{"group-" + names[i]}}
			for j := 0; j < i; j++ {
				present := graph&(1<<bit) != 0
				bit++
				if !present {
					continue
				}
				reachable[i][j] = true
				switch kind % 4 {
				case 0:
					spec.Needs = append(spec.Needs, names[j])
				case 1:
					spec.NeedsIfPresent = append(spec.NeedsIfPresent, names[j])
				case 2:
					spec.Consumes = append(spec.Consumes, nodeproto.ArtifactInput{Producer: names[j]})
				case 3:
					spec.NeedsGenerated = append(spec.NeedsGenerated, "group-"+names[j])
				}
			}
			body, err := json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('a','run',?,'pending',?,?)`, names[i], body, []byte(`{}`))
		}
		for k := range names {
			for i := range names {
				for j := range names {
					reachable[i][j] = reachable[i][j] || reachable[i][k] && reachable[k][j]
				}
			}
		}
		for i := range names {
			for j := range names {
				got, err := tenant.nodeIsAncestor(t.Context(), "run", names[j], names[i])
				if err != nil || got != reachable[i][j] {
					t.Fatalf("graph=%x kind=%d %s -> %s: %v, %v, want %v", graph, kind, names[i], names[j], got, err, reachable[i][j])
				}
			}
		}
		foreign := &Tenant{s: s, team: "b"}
		if got, err := foreign.nodeIsAncestor(t.Context(), "run", names[0], names[count-1]); err != nil || got {
			t.Fatalf("foreign ancestry=%v, %v", got, err)
		}
	})
}

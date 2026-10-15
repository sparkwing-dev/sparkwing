package nodehost

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/secretsource"
)

const bearerHashSize = sha256.Size

// Node is what one node's bearer may reach: its run and node, the secrets its
// pipeline declares, where they resolve from, and the masker of the run's logs.
type Node struct {
	Job      nodeproto.JobSpec
	Masks    *MaskRegistry
	Sink     *LogSink
	RunID    string
	NodeID   string
	Secrets  []string
	Resolver secretsource.Resolver
	Masker   *secrets.Masker
	Local    LocalOutputs
	// Refs are the other pipelines' outputs the node's plan declares it
	// reads; Local answers a latest-output read for these alone.
	Refs []nodeproto.PipelineReference
	// Children starts and reads child runs through this machine's trigger
	// queue when no controller holds the run.
	Children LocalChildren
	// Pause, when set, holds the node's no-progress timeout while a route
	// waits on its behalf; see [NodeRun.Pause].
	Pause func() (resume func())
	// Hold stops the node's wall timeout while a child it awaits waits in plan admission.
	Hold func() (resume func())
	// Output is the node's own output when the host already holds it, as after
	// a memo hit; the output route serves it to the node without the controller.
	Output json.RawMessage
}

type grant struct {
	Node
	bearer   string
	services *ProcessServices
	started  sync.Map
	// safety: revoking the grant cancels this, ending its requests in flight.
	ctx    context.Context
	cancel context.CancelFunc
}

// Admit issues a fresh bearer bound to n alone. The node process sends it as
// `Authorization: Bearer <token>`; revoke ends it, after which every request
// with it is refused.
func (s *Server) Admit(n Node) (bearer string, revoke func(), err error) {
	if n.RunID == "" || n.NodeID == "" {
		return "", nil, errors.New("node routes: run and node ids are required")
	}
	if n.Resolver == nil || n.Masker == nil {
		return "", nil, errors.New("node routes: a secret resolver and masker are required")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("node bearer: %w", err)
	}
	n.Secrets = slices.Clone(n.Secrets)
	n.Refs = slices.Clone(n.Refs)
	if n.Job.ID == "" {
		n.Job.ID = n.NodeID
	}
	if n.Job.ID != n.NodeID {
		return "", nil, errors.New("process declaration differs from its bound job")
	}
	if n.Masks == nil {
		n.Masks = NewMaskRegistry(n.Masker)
		if err := n.Masks.Accept(nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{n.Job}}, func() error { return nil }); err != nil {
			return "", nil, err
		}
	}
	g := &grant{Node: n, bearer: base64.RawURLEncoding.EncodeToString(raw)}
	g.ctx, g.cancel = context.WithCancel(s.ctx)
	g.services = &ProcessServices{lifetime: g.ctx, RunID: n.RunID, Job: n.Job, Masks: n.Masks, Secrets: append([]string(nil), n.Secrets...), Cleanups: &ProcessCleanups{}}
	g.services.Secret = func(ctx context.Context, _ capability.Authorized, name string) (string, bool, error) {
		return g.secret(ctx, name)
	}
	g.services.Output = func(ctx context.Context, _ capability.Authorized, job string) (json.RawMessage, error) {
		return g.output(ctx, job)
	}
	g.services.Latest = func(ctx context.Context, _ capability.Authorized, pipeline, job string, age time.Duration) (ProcessOutput, error) {
		return g.latest(ctx, pipeline, job, age)
	}
	g.services.StartChild = func(ctx context.Context, _ capability.Authorized, request ChildRequest) (string, error) {
		return g.startChild(ctx, request)
	}
	g.services.AwaitChild = func(ctx context.Context, _ capability.Authorized, child, job string, wait time.Duration) (ProcessChildResult, error) {
		return g.awaitChild(ctx, child, job, wait)
	}
	g.services.Annotation = g.annotation
	g.services.Summary = g.summary
	key := sha256.Sum256([]byte(g.bearer))
	s.mu.Lock()
	s.grants[key] = g
	s.cleanups.mu.Lock()
	if s.cleanups.nodes == nil {
		s.cleanups.nodes = map[[bearerHashSize]byte]*ProcessCleanups{}
	}
	s.cleanups.nodes[key] = g.services.Cleanups
	s.cleanups.mu.Unlock()
	s.mu.Unlock()
	return g.bearer, func() {
		s.mu.Lock()
		delete(s.grants, key)
		s.mu.Unlock()
		g.cancel()
	}, nil
}

// safety: grants are found by the bearer's hash, so lookup timing reveals
// nothing about the bearer, and the bearer itself is compared in constant time.
func (s *Server) authenticate(r *http.Request) *grant {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || got == "" {
		return nil
	}
	s.mu.RLock()
	g := s.grants[sha256.Sum256([]byte(got))]
	s.mu.RUnlock()
	if g == nil || subtle.ConstantTimeCompare([]byte(got), []byte(g.bearer)) != 1 {
		return nil
	}
	return g
}

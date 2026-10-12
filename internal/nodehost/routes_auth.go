package nodehost

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/secretsource"
)

const bearerHashSize = sha256.Size

// Node is what one node's bearer may reach: its run and node, the secrets its
// pipeline declares, where they resolve from, and the masker of the run's logs.
type Node struct {
	RunID    string
	NodeID   string
	Secrets  []string
	Resolver secretsource.Resolver
	Masker   *secrets.Masker
}

type grant struct {
	Node
	bearer   string
	declared map[string]bool
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
	g := &grant{Node: n, bearer: base64.RawURLEncoding.EncodeToString(raw), declared: map[string]bool{}}
	for _, name := range n.Secrets {
		g.declared[name] = true
	}
	key := sha256.Sum256([]byte(g.bearer))
	s.mu.Lock()
	s.grants[key] = g
	s.mu.Unlock()
	return g.bearer, func() {
		s.mu.Lock()
		delete(s.grants, key)
		s.mu.Unlock()
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
